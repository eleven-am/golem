package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	eventprovider "github.com/eleven-am/golem/go/internal/event/provider"
	"github.com/eleven-am/golem/go/internal/physical"
	"github.com/jmoiron/sqlx"
)

func restoreOutboxPresence(ctx context.Context, query catalogQuerier, system physical.SystemSchema) (bool, bool, error) {
	if system.Namespace.Name != "main" {
		return false, false, fmt.Errorf("sqlite restore requires main namespace")
	}
	outbox, delivery := false, false
	for _, object := range system.Objects {
		switch object.Kind {
		case physical.SystemOutbox:
			if !physical.IsOutboxSystemObjectV1(object) {
				return false, false, fmt.Errorf("sqlite restore outbox declaration is invalid")
			}
			outbox = true
		case physical.SystemOutboxDelivery:
			if !physical.IsOutboxDeliverySystemObjectV1(object) {
				return false, false, fmt.Errorf("sqlite restore delivery declaration is invalid")
			}
			delivery = true
		}
	}
	var names []string
	if err := query.SelectContext(ctx, &names, `SELECT name FROM main.sqlite_schema WHERE name IN ('_golem_outbox','_golem_outbox_delivery') AND type='table'`); err != nil {
		return false, false, err
	}
	actualOutbox, actualDelivery := false, false
	for _, name := range names {
		actualOutbox = actualOutbox || name == "_golem_outbox"
		actualDelivery = actualDelivery || name == "_golem_outbox_delivery"
	}
	if actualOutbox != outbox || actualDelivery != delivery || delivery && !outbox {
		return false, false, fmt.Errorf("sqlite restore outbox storage does not match declaration")
	}
	return outbox, delivery, nil
}

func restoreDeliveryOrphans(ctx context.Context, query sqlx.QueryerContext) error {
	var orphan bool
	if err := sqlx.GetContext(ctx, query, &orphan, `SELECT EXISTS(SELECT 1 FROM "main"."_golem_outbox_delivery" d WHERE NOT EXISTS (SELECT 1 FROM "main"."_golem_outbox" o WHERE o."causation_id"=d."causation_id"))`); err != nil {
		return err
	}
	if orphan {
		return fmt.Errorf("sqlite restore delivery group has no retained facts")
	}
	return nil
}

// WalkRestoreFacts reads every retained causation, including legacy groups
// without a delivery row, without claiming work or repairing storage. The
// callback is synchronous and owns the codec validation of this bounded group.
// The caller must have verified the archive catalog and stopped its old fleet.
func (*Provider) WalkRestoreFacts(ctx context.Context, database *sqlx.DB, system physical.SystemSchema, visit func(eventprovider.Lease) error) error {
	if ctx == nil || database == nil || visit == nil {
		return fmt.Errorf("sqlite restore fact walker configuration is invalid")
	}
	transaction, err := database.BeginTxx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return err
	}
	defer transaction.Rollback()
	outbox, delivery, err := restoreOutboxPresence(ctx, transaction, system)
	if err != nil || !outbox {
		return err
	}
	if delivery {
		if err := restoreDeliveryOrphans(ctx, transaction); err != nil {
			return err
		}
	}
	// Check key types/lengths before reading a key into Go. A malformed key must
	// not disappear behind the keyset's initial lower bound.
	var invalid bool
	if err := transaction.GetContext(ctx, &invalid, `SELECT EXISTS(SELECT 1 FROM "main"."_golem_outbox" WHERE typeof("causation_id")<>'text' OR length(CAST("causation_id" AS BLOB)) NOT BETWEEN 1 AND 256)`); err != nil {
		return err
	}
	if invalid {
		return fmt.Errorf("sqlite restore causation key is invalid")
	}
	after := ""
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var causation string
		err := transaction.GetContext(ctx, &causation, `SELECT "causation_id" FROM "main"."_golem_outbox" WHERE "causation_id">? COLLATE BINARY ORDER BY "causation_id" COLLATE BINARY LIMIT 1`, after)
		if err == sql.ErrNoRows {
			return nil
		}
		if err != nil {
			return err
		}
		if err := restoreBoundFactGroup(ctx, transaction, causation); err != nil {
			return err
		}
		var stored []sqliteFactRow
		if err := transaction.SelectContext(ctx, &stored, `SELECT "event_id","fact_version","codec_identity","generation_fingerprint","model_id","action","before_identity","after_identity","causation_id","transaction_ordinal","metadata","delete_snapshot","recorded_at" FROM "main"."_golem_outbox" WHERE "causation_id"=? ORDER BY "transaction_ordinal"`, causation); err != nil {
			return err
		}
		group := eventprovider.Lease{Facts: make([]eventprovider.FactRow, len(stored))}
		for index, row := range stored {
			if row.CausationID != causation || row.TransactionOrdinal != int64(index+1) {
				return fmt.Errorf("sqlite restore causation ordinals are incomplete")
			}
			group.Facts[index] = sqliteFactValue(row)
		}
		if delivery {
			// Operational text has its own bound; it is not fact evidence.
			if err := transaction.GetContext(ctx, &invalid, `SELECT EXISTS(SELECT 1 FROM "main"."_golem_outbox_delivery" WHERE "causation_id"=? AND (length(CAST("status" AS BLOB))>16 OR length(CAST("lease_token" AS BLOB))>128 OR length(CAST("last_failure_code" AS BLOB))>128))`, causation); err != nil {
				return err
			}
			if invalid {
				return fmt.Errorf("sqlite restore delivery headers exceed bounds")
			}
			group.Delivery, err = sqliteReadDelivery(ctx, transaction, causation)
			if err != nil && err != sql.ErrNoRows {
				return err
			}
		}
		if !delivery || err == sql.ErrNoRows {
			first := stored[0].RecordedMicros
			for _, row := range stored {
				if row.RecordedMicros < first {
					first = row.RecordedMicros
				}
			}
			stamp := time.UnixMicro(first).UTC()
			group.Delivery = eventprovider.Delivery{CausationID: causation, Status: eventprovider.StatusPending, FirstRecordedAt: stamp, AvailableAt: stamp, UpdatedAt: stamp, ImmutableFactRows: len(stored)}
		}
		if err := visit(group); err != nil {
			return err
		}
		after = causation
	}
}

func restoreBoundFactGroup(ctx context.Context, query sqlx.QueryerContext, causation string) error {
	const maximumHeaderBytes = eventprovider.MaximumCausationFacts * 6 * 256
	var evidence, headers, invalid []string
	for _, column := range []string{"before_identity", "after_identity", "metadata", "delete_snapshot"} {
		name := `"` + column + `"`
		evidence = append(evidence, `COALESCE(length(`+name+`),0)`)
		invalid = append(invalid, `(typeof(`+name+`) NOT IN ('blob','null'))`)
	}
	invalid = append(invalid, `("metadata" IS NULL)`)
	for _, column := range []string{"event_id", "causation_id", "generation_fingerprint", "model_id", "codec_identity", "action"} {
		name := `"` + column + `"`
		headers = append(headers, `length(CAST(`+name+` AS BLOB))`)
		invalid = append(invalid, `(typeof(`+name+`)<>'text' OR length(CAST(`+name+` AS BLOB)) NOT BETWEEN 1 AND 256)`)
	}
	for _, column := range []string{"fact_version", "transaction_ordinal", "recorded_at"} {
		invalid = append(invalid, `(typeof("`+column+`")<>'integer')`)
	}
	var bound struct{ Count, Evidence, Headers, Invalid int64 }
	statement := `SELECT COUNT(*) AS count,COALESCE(SUM(evidence),0) AS evidence,COALESCE(SUM(headers),0) AS headers,COALESCE(SUM(invalid),0) AS invalid FROM (SELECT (` + strings.Join(evidence, "+") + `) AS evidence,(` + strings.Join(headers, "+") + `) AS headers,(` + strings.Join(invalid, " OR ") + `) AS invalid FROM "main"."_golem_outbox" WHERE "causation_id"=? ORDER BY "transaction_ordinal" LIMIT ?)`
	if err := sqlx.GetContext(ctx, query, &bound, statement, causation, eventprovider.MaximumCausationFacts+1); err != nil {
		return err
	}
	if bound.Count < 1 || bound.Count > eventprovider.MaximumCausationFacts || bound.Evidence > eventprovider.MaximumCausationEvidenceBytes || bound.Headers > maximumHeaderBytes || bound.Invalid != 0 {
		return fmt.Errorf("sqlite restore causation exceeds bounds or has invalid storage types")
	}
	return nil
}
