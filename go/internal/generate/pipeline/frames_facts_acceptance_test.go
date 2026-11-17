package pipeline

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	compilerir "github.com/eleven-am/golem/go/internal/compiler/ir"
)

func TestFreshGeneratedFramesFactsReadAndWriteEquivalence(t *testing.T) {
	if os.Getenv("GOLEM_POLICY_HISTORY_WRITER_REHEARSAL") != "4" {
		t.Skip("requires an explicitly compiled writer-4 rehearsal; reader-3 does not certify full Frames fact acceptance")
	}
	runGeneratedDTOConsumerWithProviders(t, "framesfacts", nil, framesFactsConsumerTest, compilerir.SQLite, compilerir.PostgreSQL)
}

const framesFactsConsumerTest = `package acceptance_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	app "example.test/home"
	"github.com/eleven-am/golem/go/events"
	"github.com/eleven-am/golem/go/golem"
	"github.com/eleven-am/golem/go/observe"
	"github.com/eleven-am/golem/go/provider"
	providerpostgres "github.com/eleven-am/golem/go/provider/postgresql"
	providersqlite "github.com/eleven-am/golem/go/provider/sqlite"
	golemruntime "github.com/eleven-am/golem/go/runtime"
)

func framesID(number uint32) golem.UUID {
	var result golem.UUID
	binary.BigEndian.PutUint32(result[12:], number)
	return result
}

func framesIDs(cells []uint32, bases ...uint32) []golem.UUID {
	var result []golem.UUID
	for _, base := range bases {
		for _, cell := range cells {
			result = append(result, framesID(base+cell))
		}
	}
	return result
}

type framesGraphQLPrincipalKey struct{}

func framesGraphQLResponse(t *testing.T, ctx context.Context, handler http.Handler, principal app.Principal, query string) map[string]any {
	t.Helper()
	body, err := json.Marshal(map[string]any{"query": query})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/graphql", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request = request.WithContext(context.WithValue(ctx, framesGraphQLPrincipalKey{}, principal))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("GraphQL HTTP status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("GraphQL JSON: %v", err)
	}
	return response
}

func framesGraphQLData(t *testing.T, ctx context.Context, handler http.Handler, principal app.Principal, query string) map[string]any {
	t.Helper()
	response := framesGraphQLResponse(t, ctx, handler, principal, query)
	if response["errors"] != nil {
		t.Fatalf("GraphQL query failed: %#v", response)
	}
	data, ok := response["data"].(map[string]any)
	if !ok {
		t.Fatalf("GraphQL data is not an object: %#v", response)
	}
	return data
}

func framesGraphQLRows(t *testing.T, label string, value any) []map[string]any {
	t.Helper()
	items, ok := value.([]any)
	if !ok {
		t.Fatalf("%s is not a list: %#v", label, value)
	}
	rows := make([]map[string]any, len(items))
	for index, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("%s[%d] is not an object: %#v", label, index, item)
		}
		rows[index] = row
	}
	return rows
}

func framesGraphQLIDs(t *testing.T, label string, value any, expected []golem.UUID) {
	t.Helper()
	want := make(map[string]bool, len(expected))
	for _, id := range expected {
		want[id.String()] = true
	}
	rows := framesGraphQLRows(t, label, value)
	seen := make(map[string]bool, len(rows))
	for _, row := range rows {
		id, ok := row["id"].(string)
		if !ok || !want[id] || seen[id] {
			t.Fatalf("%s unexpected/missing/duplicate ID: %#v", label, row)
		}
		seen[id] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("%s IDs=%v want=%v", label, seen, expected)
	}
}

func framesGraphQLSelections(t *testing.T, ctx context.Context, handler http.Handler, application *app.App[app.Principal]) {
	t.Helper()
	t.Run("nested-source-ownership", func(t *testing.T) {
		query := fmt.Sprintf("{ medias(where: {id: {equals: %q}}, take: 1) { id videos(where: {id: {equals: %q}}, take: 1) { id watchProgresses(take: 3) { id } playlistVideos(take: 3) { id } } } }", framesID(1).String(), framesID(101).String())
		data := framesGraphQLData(t, ctx, handler, "member", query)
		framesGraphQLIDs(t, "nested Media1", data["medias"], []golem.UUID{framesID(1)})
		media := framesGraphQLRows(t, "nested Media1", data["medias"])[0]
		framesGraphQLIDs(t, "nested Video101", media["videos"], []golem.UUID{framesID(101)})
		video := framesGraphQLRows(t, "nested Video101", media["videos"])[0]
		framesGraphQLIDs(t, "owned progress only", video["watchProgresses"], []golem.UUID{framesID(301)})
		framesGraphQLIDs(t, "own and shared playlist only", video["playlistVideos"], []golem.UUID{framesID(401), framesID(801)})
	})
	t.Run("physical-fact-does-not-declassify-selected-target", func(t *testing.T) {
		// The readable scalar query succeeds through physical MISSING copy502.
		// Selecting its deny-all target retains the ordinary NOT_GRANTED
		// contract; the fact does not declassify target rows or their fields.
		query := fmt.Sprintf("{ medias(where: {id: {equals: %q}}, take: 1) { id videos(where: {id: {equals: %q}}, take: 1) { id } } }", framesID(2).String(), framesID(102).String())
		data := framesGraphQLData(t, ctx, handler, "member", query)
		framesGraphQLIDs(t, "MISSING-copy-granted Media2", data["medias"], []golem.UUID{framesID(2)})
		media := framesGraphQLRows(t, "Media2", data["medias"])[0]
		framesGraphQLIDs(t, "MISSING-copy-granted Video102", media["videos"], []golem.UUID{framesID(102)})
		query = fmt.Sprintf("{ medias(where: {id: {equals: %q}}, take: 1) { id videos(where: {id: {equals: %q}}, take: 1) { id storageObjects(take: 1) { id } } } }", framesID(2).String(), framesID(102).String())
		response := framesGraphQLResponse(t, ctx, handler, "member", query)
		failures, ok := response["errors"].([]any)
		if !ok || len(failures) != 1 || response["data"] != nil {
			t.Fatalf("selected hidden target must return exactly one denial and no data: %#v", response)
		}
		failure, ok := failures[0].(map[string]any)
		if !ok {
			t.Fatalf("GraphQL denial is not an object: %#v", failures[0])
		}
		extensions, ok := failure["extensions"].(map[string]any)
		if !ok || len(extensions) != 2 || extensions["code"] != "FORBIDDEN" || extensions["reason"] != "NOT_GRANTED" || failure["message"] != "operation is not granted" {
			t.Fatalf("selected hidden target returned a different refusal: %#v", failure)
		}
		path, ok := failure["path"].([]any)
		if !ok || len(path) != 1 || path[0] != "medias" {
			t.Fatalf("selected target denial lost its root path: %#v", failure)
		}
		caller, err := application.ForPrincipal(ctx, "member")
		if err != nil {
			t.Fatal(err)
		}
		projection := golem.Select[app.Media](app.Medias.ID, app.Medias.Videos.Args(golem.Where(app.Videos.ID.Eq(framesID(102))), golem.Select[app.Video](app.Videos.ID, app.Videos.StorageObjects.Args(golem.Select[app.StorageObject](app.StorageObjects.ID)))))
		rows, err := caller.Medias.FindMany(ctx, golem.Where(app.Medias.ID.Eq(framesID(2))), projection)
		var denied *golem.Error
		if len(rows) != 0 || !errors.As(err, &denied) || denied.Code != golem.CodeForbidden || denied.Reason != golem.ReasonNotGranted {
			t.Fatalf("SDK selected-target denial differs from HTTP: rows=%d err=%v", len(rows), err)
		}
	})
	t.Run("ordinary-empty-some-keeps-target-authorization", func(t *testing.T) {
		query := fmt.Sprintf("{ videos(where: {AND: [{id: {equals: %q}}, {storageObjects: {some: {}}}]}) { id } }", framesID(102).String())
		data := framesGraphQLData(t, ctx, handler, "member", query)
		framesGraphQLIDs(t, "ordinary hidden-target existence", data["videos"], nil)
	})
}

func framesActorSnapshot(source app.Actor) (app.Actor, error) {
	source.DomainStorageIDs = append([]golem.UUID(nil), source.DomainStorageIDs...)
	source.GrantedStorageIDs = append([]golem.UUID(nil), source.GrantedStorageIDs...)
	source.OpenStorageIDs = append([]golem.UUID(nil), source.OpenStorageIDs...)
	source.RestrictedStorageIDs = append([]golem.UUID(nil), source.RestrictedStorageIDs...)
	source.DeniedStorageIDs = append([]golem.UUID(nil), source.DeniedStorageIDs...)
	source.AllowedMediaIDs = append([]golem.UUID(nil), source.AllowedMediaIDs...)
	source.DeniedMediaIDs = append([]golem.UUID(nil), source.DeniedMediaIDs...)
	source.RemovedMediaIDs = append([]golem.UUID(nil), source.RemovedMediaIDs...)
	source.OwnerExcludedMediaIDs = append([]golem.UUID(nil), source.OwnerExcludedMediaIDs...)
	source.OwnedProfileIDs = append([]golem.UUID(nil), source.OwnedProfileIDs...)
	source.VisiblePlaylistIDs = append([]golem.UUID(nil), source.VisiblePlaylistIDs...)
	source.OwnedPlaylistIDs = append([]golem.UUID(nil), source.OwnedPlaylistIDs...)
	return source, nil
}

type framesOperationAudit struct {
	escapes        atomic.Int64
	storageObject  golem.ModelID
	completed      atomic.Int64
	armDepth       atomic.Bool
	initialPending chan int64
	wake           chan struct{}
}

func (audit *framesOperationAudit) ObserveGolem(_ context.Context, value observe.Observation) {
	if value.Operation() == observe.OperationSystemEscape {
		audit.escapes.Add(1)
	}
	if value.Operation() == observe.OperationEventDepthPending && audit.armDepth.CompareAndSwap(true, false) {
		audit.initialPending <- value.AggregateCount()
	}
	if value.ModelID() == audit.storageObject && (value.Operation() == observe.OperationSubscriptionSuppression || value.Operation() == observe.OperationSubscriptionEvaluation && value.Outcome() == observe.OutcomeFailure) {
		audit.completed.Add(1)
	}
	select {
	case audit.wake <- struct{}{}:
	default:
	}
}

type framesPublication struct {
	causation golem.CausationID
	changes   []framesPublishedChange
}
type framesPublishedChange struct {
	model  golem.ModelID
	action golem.EventAction
}

type framesEventTransport struct {
	events.EventTransport
	accepted chan framesPublication
	gate     atomic.Pointer[framesPublishGate]
}

type framesPublishGate struct {
	model   golem.ModelID
	entered chan golem.CausationID
	forward chan struct{}
	finish  chan struct{}
}

func (transport *framesEventTransport) TransportCapabilities() events.TransportCapabilities {
	return events.CapabilitiesOf(transport.EventTransport)
}

func (transport *framesEventTransport) Publish(ctx context.Context, batch events.EventBatch) error {
	publication := framesPublication{causation: batch.CausationID()}
	for _, notice := range batch.Events() {
		publication.changes = append(publication.changes, framesPublishedChange{notice.ModelID(), notice.Action()})
	}
	var held *framesPublishGate
	if gate := transport.gate.Load(); gate != nil && len(publication.changes) == 1 && publication.changes[0].model == gate.model && publication.changes[0].action == golem.EventUpdated && transport.gate.CompareAndSwap(gate, nil) {
		held = gate
		select {
		case held.entered <- publication.causation:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-held.forward:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := transport.EventTransport.Publish(ctx, batch); err != nil {
		return err
	}
	select {
	case transport.accepted <- publication:
	case <-ctx.Done():
		return ctx.Err()
	}
	if held != nil {
		select {
		case <-held.finish:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func framesWait(t *testing.T, ctx context.Context, wake <-chan struct{}, label string, complete func() bool) {
	t.Helper()
	for !complete() {
		select {
		case <-wake:
		case <-ctx.Done():
			t.Fatalf("%s: %v", label, ctx.Err())
		}
	}
}

func framesAwaitDelivered(t *testing.T, ctx context.Context, application *app.App[app.Principal], audit *framesOperationAudit, causation golem.CausationID) {
	t.Helper()
	framesWait(t, ctx, audit.wake, "outbox acknowledgement", func() bool {
		delivery, err := application.EventOperator().Inspect(ctx, causation)
		if err != nil {
			t.Fatal(err)
		}
		return delivery.Status() == events.DeliveryDelivered
	})
}

func framesAwaitPublication(t *testing.T, ctx context.Context, transport *framesEventTransport, model golem.ModelID, action golem.EventAction) golem.CausationID {
	t.Helper()
	select {
	case publication := <-transport.accepted:
		if len(publication.changes) != 1 || publication.changes[0].model != model || publication.changes[0].action != action {
			t.Fatalf("unexpected publication after drained setup: %#v", publication.changes)
		}
		return publication.causation
	case <-ctx.Done():
		t.Fatalf("native mutation was not published: %v", ctx.Err())
		return golem.CausationID{}
	}
}

func framesStartPublisherAndDrain(t *testing.T, ctx context.Context, application *app.App[app.Principal], transport *framesEventTransport, audit *framesOperationAudit) {
	t.Helper()
	publisherCtx, stopPublisher := context.WithCancel(ctx)
	publisherDone := make(chan error, 1)
	audit.armDepth.Store(true)
	go func() { publisherDone <- application.RunEventPublisher(publisherCtx) }()
	t.Cleanup(func() {
		stopPublisher()
		select {
		case err := <-publisherDone:
			if err != nil {
				t.Errorf("publisher shutdown: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Error("publisher did not stop")
		}
	})
	wait, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var pending int64
	select {
	case pending = <-audit.initialPending:
	case <-wait.Done():
		t.Fatal("publisher did not report its initial pending snapshot")
	}
	if pending < 0 || pending > 1024 {
		t.Fatalf("unexpected seed pending depth %d", pending)
	}
	// Both providers snapshot pending depth after leasing. One worker leases
	// one group, and no setup writes run while this exact backlog is drained.
	seen := map[golem.CausationID]bool{}
	var last golem.CausationID
	for int64(len(seen)) < pending+1 {
		select {
		case publication := <-transport.accepted:
			seen[publication.causation] = true
			last = publication.causation
		case <-wait.Done():
			t.Fatalf("seed outbox drain accepted %d of %d groups", len(seen), pending+1)
		}
	}
	framesAwaitDelivered(t, wait, application, audit, last)
}

func framesExpectAttributedResync[E any](t *testing.T, ctx context.Context, label string, stream golem.EventStream[E]) {
	t.Helper()
	framesExpectResyncReason(t, ctx, label, stream, events.ReasonDependencyChanged)
}

func framesExpectResyncReason[E any](t *testing.T, ctx context.Context, label string, stream golem.EventStream[E], expected events.Reason) {
	t.Helper()
	wait, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, err := stream.Recv(wait)
	code, _ := events.CodeOf(err)
	reason, _ := events.ReasonOf(err)
	if code != events.CodeSubscriptionResync || reason != expected {
		t.Errorf("%s: expected resync reason=%q, got code=%s reason=%q err=%v", label, expected, code, reason, err)
	}
}

func framesExpectQuietAfterEvaluation[E any](t *testing.T, ctx context.Context, label string, stream golem.EventStream[E]) {
	t.Helper()
	// Every physical dependency evaluation has completed before this receive.
	// The deadline checks the completed queue, not an assumed processing time.
	wait, cancel := context.WithTimeout(ctx, 20*time.Millisecond)
	defer cancel()
	_, err := stream.Recv(wait)
	if code, _ := events.CodeOf(err); code != events.CodeSubscriptionCancelled {
		t.Errorf("%s: unrelated authority received activity or termination after completed evaluation: %v", label, err)
	}
}

func framesNativeFactEventGain(t *testing.T, ctx context.Context, application *app.App[app.Principal], transport *framesEventTransport, audit *framesOperationAudit, actors map[string]app.Actor) {
	t.Helper()
	system := application.System()
	storageF := framesID(1006)
	actors["event-outsider"] = app.Actor{DomainStorageIDs: []golem.UUID{storageF}, GrantedStorageIDs: []golem.UUID{storageF}, DeniedStorageIDs: []golem.UUID{framesID(1001)}}
	t.Cleanup(func() { delete(actors, "event-outsider") })
	// F observes the shared title through this sibling, but never owns the
	// tested Video112 file domain. Holding copy A later denies the whole title.
	if _, err := system.Videos.Create(ctx, app.Videos.Create(app.Videos.ID.Create(framesID(212)), app.Videos.MediaID.Create(framesID(12)), app.Videos.StorageID.Create(storageF))); err != nil {
		t.Fatal(err)
	}
	member, err := application.ForPrincipal(ctx, "member")
	if err != nil {
		t.Fatal(err)
	}
	other, err := application.ForPrincipal(ctx, "event-outsider")
	if err != nil {
		t.Fatal(err)
	}
	guest, err := application.ForPrincipal(ctx, "guest")
	if err != nil {
		t.Fatal(err)
	}
	framesStartPublisherAndDrain(t, ctx, application, transport, audit)
	for _, wave := range []struct {
		name          string
		action        golem.EventAction
		before, after bool
		memberReason  events.Reason
	}{
		{"update-hidden-copy-gains-membership", golem.EventUpdated, false, true, events.ReasonDependencyChanged},
		{"delete-hidden-copy-revokes-membership", golem.EventDeleted, true, false, ""},
		{"recreate-hidden-copy-restores-membership", golem.EventCreated, false, true, events.ReasonDependencyChanged},
	} {
		t.Run(wave.name, func(t *testing.T) {
			memberBefore, memberAfter := int64(0), int64(0)
			if wave.before {
				memberBefore = 1
			}
			if wave.after {
				memberAfter = 1
			}
			mediaWhere := golem.Where(app.Medias.ID.Eq(framesID(12)))
			videoWhere := golem.Where(app.Videos.ID.Eq(framesID(112)))
			assertCount := func(label string, expected int64, query func() (int64, error)) {
				if count, err := query(); err != nil || count != expected {
					t.Fatalf("%s count=%d err=%v expected=%d", label, count, err, expected)
				}
			}
			assertCount("member Media before", memberBefore, func() (int64, error) { return member.Medias.Count(ctx, mediaWhere) })
			assertCount("member Video before", memberBefore, func() (int64, error) { return member.Videos.Count(ctx, videoWhere) })
			assertCount("outsider Media before", 1-memberBefore, func() (int64, error) { return other.Medias.Count(ctx, mediaWhere) })
			assertCount("outsider Video before", 0, func() (int64, error) { return other.Videos.Count(ctx, videoWhere) })
			assertCount("outsider sibling Video before", 1-memberBefore, func() (int64, error) { return other.Videos.Count(ctx, golem.Where(app.Videos.ID.Eq(framesID(212)))) })
			assertCount("Guest Video before", 0, func() (int64, error) { return guest.Videos.Count(ctx, videoWhere) })

			assertSources := func(phase string, granted bool) {
				framesAssertEventSourceVisibility(t, ctx, "member Episode212 "+phase, member.Episodes, app.Episodes.ID.Eq(framesID(212)), app.Episodes.ByID.Value(framesID(212)), granted)
				framesAssertEventSourceVisibility(t, ctx, "member owned Progress312 "+phase, member.WatchProgresses, app.WatchProgresses.ID.Eq(framesID(312)), app.WatchProgresses.ByID.Value(framesID(312)), granted)
				framesAssertEventSourceVisibility(t, ctx, "member foreign Progress612 "+phase, member.WatchProgresses, app.WatchProgresses.ID.Eq(framesID(612)), app.WatchProgresses.ByID.Value(framesID(612)), false)
				framesAssertEventSourceVisibility(t, ctx, "member own Playlist412 "+phase, member.PlaylistVideos, app.PlaylistVideos.ID.Eq(framesID(412)), app.PlaylistVideos.ByID.Value(framesID(412)), granted)
				framesAssertEventSourceVisibility(t, ctx, "member shared-readable Playlist812 "+phase, member.PlaylistVideos, app.PlaylistVideos.ID.Eq(framesID(812)), app.PlaylistVideos.ByID.Value(framesID(812)), granted)
				framesAssertEventSourceVisibility(t, ctx, "member foreign Playlist712 "+phase, member.PlaylistVideos, app.PlaylistVideos.ID.Eq(framesID(712)), app.PlaylistVideos.ByID.Value(framesID(712)), false)
			}
			assertSources("before", wave.before)

			wait, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			memberMedia, err := member.Medias.Events(ctx, golem.EventWhere(app.Medias.ID.Eq(framesID(12))))
			if err != nil {
				t.Fatalf("member Media subscription: %v", err)
			}
			defer memberMedia.Close()
			memberVideo, err := member.Videos.Events(ctx, golem.EventWhere(app.Videos.ID.Eq(framesID(112))))
			if err != nil {
				t.Fatalf("member Video subscription: %v", err)
			}
			defer memberVideo.Close()
			memberFilteredVideo, err := member.Videos.Events(ctx, golem.EventWhere(app.Videos.ID.Eq(framesID(112)).And(app.Videos.Media.Is(app.Medias.ID.Eq(framesID(12))))))
			if err != nil {
				t.Fatalf("member relation-filtered Video subscription: %v", err)
			}
			defer memberFilteredVideo.Close()
			otherMedia, err := other.Medias.Events(ctx, golem.EventWhere(app.Medias.ID.Eq(framesID(12))))
			if err != nil {
				t.Fatalf("outsider Media subscription: %v", err)
			}
			defer otherMedia.Close()
			otherVideo, err := other.Videos.Events(ctx, golem.EventWhere(app.Videos.ID.Eq(framesID(112))))
			if err != nil {
				t.Fatalf("outsider Video subscription: %v", err)
			}
			defer otherVideo.Close()
			// A separate filter-derived Media fact question must not lose the
			// source Video's own mandatory scope when candidate questions are ORed.
			otherFilteredVideo, err := other.Videos.Events(ctx, golem.EventWhere(app.Videos.ID.Eq(framesID(112)).And(app.Videos.Media.Is(app.Medias.ID.Eq(framesID(12))))))
			if err != nil {
				t.Fatalf("outsider relation-filtered Video subscription: %v", err)
			}
			defer otherFilteredVideo.Close()
			otherSibling, err := other.Videos.Events(ctx, golem.EventWhere(app.Videos.ID.Eq(framesID(212))))
			if err != nil {
				t.Fatalf("outsider sibling Video subscription: %v", err)
			}
			defer otherSibling.Close()
			guestVideo, err := guest.Videos.Events(ctx, golem.EventWhere(app.Videos.ID.Eq(framesID(112))))
			if err != nil {
				t.Fatalf("Guest Video subscription: %v", err)
			}
			defer guestVideo.Close()
			// Uncaptured source models retain their own ordinary authorization before
			// the certified Video cut, both with and without a caller relation filter.
			memberEpisode, err := member.Episodes.Events(ctx, golem.EventWhere(app.Episodes.ID.Eq(framesID(212))))
			if err != nil {
				t.Fatalf("member Episode212 subscription: %v", err)
			}
			defer memberEpisode.Close()
			memberEpisodeFiltered, err := member.Episodes.Events(ctx, golem.EventWhere(app.Episodes.ID.Eq(framesID(212)).And(app.Episodes.Video.Is(app.Videos.ID.Eq(framesID(112))))))
			if err != nil {
				t.Fatalf("member Episode212 relation-filtered subscription: %v", err)
			}
			defer memberEpisodeFiltered.Close()
			ownedProgress, err := member.WatchProgresses.Events(ctx, golem.EventWhere(app.WatchProgresses.ID.Eq(framesID(312))))
			if err != nil {
				t.Fatalf("member owned Progress312 subscription: %v", err)
			}
			defer ownedProgress.Close()
			ownedProgressFiltered, err := member.WatchProgresses.Events(ctx, golem.EventWhere(app.WatchProgresses.ID.Eq(framesID(312)).And(app.WatchProgresses.Video.Is(app.Videos.ID.Eq(framesID(112))))))
			if err != nil {
				t.Fatalf("member owned Progress312 relation-filtered subscription: %v", err)
			}
			defer ownedProgressFiltered.Close()
			foreignProgress, err := member.WatchProgresses.Events(ctx, golem.EventWhere(app.WatchProgresses.ID.Eq(framesID(612))))
			if err != nil {
				t.Fatalf("member foreign Progress612 subscription: %v", err)
			}
			defer foreignProgress.Close()
			foreignProgressFiltered, err := member.WatchProgresses.Events(ctx, golem.EventWhere(app.WatchProgresses.ID.Eq(framesID(612)).And(app.WatchProgresses.Video.Is(app.Videos.ID.Eq(framesID(112))))))
			if err != nil {
				t.Fatalf("member foreign Progress612 relation-filtered subscription: %v", err)
			}
			defer foreignProgressFiltered.Close()
			ownPlaylist, err := member.PlaylistVideos.Events(ctx, golem.EventWhere(app.PlaylistVideos.ID.Eq(framesID(412))))
			if err != nil {
				t.Fatalf("member own Playlist412 subscription: %v", err)
			}
			defer ownPlaylist.Close()
			ownPlaylistFiltered, err := member.PlaylistVideos.Events(ctx, golem.EventWhere(app.PlaylistVideos.ID.Eq(framesID(412)).And(app.PlaylistVideos.Video.Is(app.Videos.ID.Eq(framesID(112))))))
			if err != nil {
				t.Fatalf("member own Playlist412 relation-filtered subscription: %v", err)
			}
			defer ownPlaylistFiltered.Close()
			sharedPlaylist, err := member.PlaylistVideos.Events(ctx, golem.EventWhere(app.PlaylistVideos.ID.Eq(framesID(812))))
			if err != nil {
				t.Fatalf("member shared-readable Playlist812 subscription: %v", err)
			}
			defer sharedPlaylist.Close()
			sharedPlaylistFiltered, err := member.PlaylistVideos.Events(ctx, golem.EventWhere(app.PlaylistVideos.ID.Eq(framesID(812)).And(app.PlaylistVideos.Video.Is(app.Videos.ID.Eq(framesID(112))))))
			if err != nil {
				t.Fatalf("member shared-readable Playlist812 relation-filtered subscription: %v", err)
			}
			defer sharedPlaylistFiltered.Close()
			foreignPlaylist, err := member.PlaylistVideos.Events(ctx, golem.EventWhere(app.PlaylistVideos.ID.Eq(framesID(712))))
			if err != nil {
				t.Fatalf("member foreign Playlist712 subscription: %v", err)
			}
			defer foreignPlaylist.Close()
			foreignPlaylistFiltered, err := member.PlaylistVideos.Events(ctx, golem.EventWhere(app.PlaylistVideos.ID.Eq(framesID(712)).And(app.PlaylistVideos.Video.Is(app.Videos.ID.Eq(framesID(112))))))
			if err != nil {
				t.Fatalf("member foreign Playlist712 relation-filtered subscription: %v", err)
			}
			defer foreignPlaylistFiltered.Close()
			before := audit.completed.Load()
			switch wave.action {
			case golem.EventUpdated:
				_, err = system.StorageObjects.Update(ctx, app.StorageObjects.ByID.Value(framesID(512)), app.StorageObjects.Update(app.StorageObjects.State.Set("MISSING")))
			case golem.EventDeleted:
				_, err = system.StorageObjects.Delete(ctx, app.StorageObjects.ByID.Value(framesID(512)))
			case golem.EventCreated:
				_, err = system.StorageObjects.Create(ctx, app.StorageObjects.Create(app.StorageObjects.ID.Create(framesID(512)), app.StorageObjects.VideoID.Create(framesID(112)), app.StorageObjects.StorageID.Create(framesID(1001)), app.StorageObjects.State.Create("MISSING")))
			default:
				t.Fatal("unsupported hidden-copy wave")
			}
			if err != nil {
				t.Fatalf("native hidden-copy mutation: %v", err)
			}
			causation := framesAwaitPublication(t, wait, transport, audit.storageObject, wave.action)
			framesExpectResyncReason(t, ctx, "member Media12 visibility transition", memberMedia, wave.memberReason)
			framesExpectResyncReason(t, ctx, "member Video112 own scope AND Media side scope", memberVideo, wave.memberReason)
			framesExpectResyncReason(t, ctx, "member relation-filtered Video112 retains source scope", memberFilteredVideo, wave.memberReason)
			framesExpectAttributedResync(t, ctx, "outsider Media12 negative DENY transition", otherMedia)
			framesExpectAttributedResync(t, ctx, "outsider sibling Video212 recorded Media side transition", otherSibling)
			framesExpectResyncReason(t, ctx, "member Episode212 visibility transition", memberEpisode, wave.memberReason)
			framesExpectResyncReason(t, ctx, "member Episode212 relation-filtered visibility transition", memberEpisodeFiltered, wave.memberReason)
			framesExpectResyncReason(t, ctx, "member owned Progress312 visibility transition", ownedProgress, wave.memberReason)
			framesExpectResyncReason(t, ctx, "member owned Progress312 relation-filtered visibility transition", ownedProgressFiltered, wave.memberReason)
			framesExpectResyncReason(t, ctx, "member own Playlist412 visibility transition", ownPlaylist, wave.memberReason)
			framesExpectResyncReason(t, ctx, "member own Playlist412 relation-filtered visibility transition", ownPlaylistFiltered, wave.memberReason)
			framesExpectResyncReason(t, ctx, "member shared-readable Playlist812 visibility transition", sharedPlaylist, wave.memberReason)
			framesExpectResyncReason(t, ctx, "member shared-readable Playlist812 relation-filtered visibility transition", sharedPlaylistFiltered, wave.memberReason)
			framesWait(t, wait, audit.wake, "twenty hidden-copy dependency completions", func() bool { return audit.completed.Load() >= before+20 })
			framesExpectQuietAfterEvaluation(t, ctx, "outsider Video112 false own scope", otherVideo)
			framesExpectQuietAfterEvaluation(t, ctx, "outsider relation-filtered Video112 cannot borrow Media12 scope", otherFilteredVideo)
			framesExpectQuietAfterEvaluation(t, ctx, "Guest Video112 outside both scopes", guestVideo)
			framesExpectQuietAfterEvaluation(t, ctx, "member foreign Progress612 retains source authority", foreignProgress)
			framesExpectQuietAfterEvaluation(t, ctx, "member foreign Progress612 relation-filtered retains source authority", foreignProgressFiltered)
			framesExpectQuietAfterEvaluation(t, ctx, "member foreign Playlist712 retains source authority", foreignPlaylist)
			framesExpectQuietAfterEvaluation(t, ctx, "member foreign Playlist712 relation-filtered retains source authority", foreignPlaylistFiltered)
			assertCount("member Media after", memberAfter, func() (int64, error) { return member.Medias.Count(ctx, mediaWhere) })
			assertCount("member Video after", memberAfter, func() (int64, error) { return member.Videos.Count(ctx, videoWhere) })
			assertCount("outsider Media after", 1-memberAfter, func() (int64, error) { return other.Medias.Count(ctx, mediaWhere) })
			assertCount("outsider Video after", 0, func() (int64, error) { return other.Videos.Count(ctx, videoWhere) })
			assertCount("outsider sibling Video after", 1-memberAfter, func() (int64, error) { return other.Videos.Count(ctx, golem.Where(app.Videos.ID.Eq(framesID(212)))) })
			assertSources("after", wave.after)
			framesAssertEventSourceVisibility(t, ctx, "System member Episode212 survives", system.Episodes, app.Episodes.ID.Eq(framesID(212)), app.Episodes.ByID.Value(framesID(212)), true)
			framesAssertEventSourceVisibility(t, ctx, "System member owned Progress312 survives", system.WatchProgresses, app.WatchProgresses.ID.Eq(framesID(312)), app.WatchProgresses.ByID.Value(framesID(312)), true)
			framesAssertEventSourceVisibility(t, ctx, "System member foreign Progress612 survives", system.WatchProgresses, app.WatchProgresses.ID.Eq(framesID(612)), app.WatchProgresses.ByID.Value(framesID(612)), true)
			framesAssertEventSourceVisibility(t, ctx, "System member own Playlist412 survives", system.PlaylistVideos, app.PlaylistVideos.ID.Eq(framesID(412)), app.PlaylistVideos.ByID.Value(framesID(412)), true)
			framesAssertEventSourceVisibility(t, ctx, "System member shared-readable Playlist812 survives", system.PlaylistVideos, app.PlaylistVideos.ID.Eq(framesID(812)), app.PlaylistVideos.ByID.Value(framesID(812)), true)
			framesAssertEventSourceVisibility(t, ctx, "System member foreign Playlist712 survives", system.PlaylistVideos, app.PlaylistVideos.ID.Eq(framesID(712)), app.PlaylistVideos.ByID.Value(framesID(712)), true)
			copyRow, copyErr := system.StorageObjects.FindUnique(ctx, app.StorageObjects.ByID.Value(framesID(512)))
			if wave.action == golem.EventDeleted {
				if !errors.Is(copyErr, golem.CodeNotFound) {
					t.Fatalf("deleted hidden copy still present: %v", copyErr)
				}
			} else {
				if copyErr != nil {
					t.Fatal(copyErr)
				}
				if value, present := golem.Value(copyRow, app.StorageObjects.VideoID).Get(); !present || value != framesID(112) {
					t.Fatalf("hidden copy VideoID=%v present=%t", value, present)
				}
				if value, present := golem.Value(copyRow, app.StorageObjects.StorageID).Get(); !present || value != framesID(1001) {
					t.Fatalf("hidden copy StorageID=%v present=%t", value, present)
				}
				if value, present := golem.Value(copyRow, app.StorageObjects.State).Get(); !present || value != "MISSING" {
					t.Fatalf("hidden copy state=%v present=%t", value, present)
				}
			}
			if _, err := member.StorageObjects.FindUnique(ctx, app.StorageObjects.ByID.Value(framesID(512))); !errors.Is(err, golem.CodeForbidden) {
				t.Fatalf("hidden target became caller-readable: %v", err)
			}
			framesAwaitDelivered(t, wait, application, audit, causation)
		})
	}
}

func framesNativeFactEventDelayedReparent(t *testing.T, ctx context.Context, application *app.App[app.Principal], transport *framesEventTransport, audit *framesOperationAudit, actors map[string]app.Actor) {
	t.Helper()
	system := application.System()
	storageF, storageG := framesID(1006), framesID(1007)
	actors["delayed-old-FG"] = app.Actor{DomainStorageIDs: []golem.UUID{storageF, storageG}, GrantedStorageIDs: []golem.UUID{storageF, storageG}, DeniedStorageIDs: []golem.UUID{framesID(1001)}, DeniedMediaIDs: []golem.UUID{framesID(13)}}
	actors["delayed-old-F"] = app.Actor{DomainStorageIDs: []golem.UUID{storageF}, GrantedStorageIDs: []golem.UUID{storageF}, DeniedStorageIDs: []golem.UUID{framesID(1001)}}
	// H is absent from old Media12. Keep a real nonempty physical question
	// without granting any part of A's historical authority domain.
	actors["delayed-new"] = app.Actor{AllowedMediaIDs: []golem.UUID{framesID(13)}, DomainStorageIDs: []golem.UUID{framesID(1008)}, GrantedStorageIDs: []golem.UUID{framesID(1008)}}
	t.Cleanup(func() {
		delete(actors, "delayed-old-FG")
		delete(actors, "delayed-old-F")
		delete(actors, "delayed-new")
	})
	if _, err := system.Medias.Create(ctx, app.Medias.Create(app.Medias.ID.Create(framesID(13)), app.Medias.Title.Create("delayed-destination"), app.Medias.AgeRating.Create(12))); err != nil {
		t.Fatal(err)
	}
	if _, err := system.Videos.Create(ctx, app.Videos.Create(app.Videos.ID.Create(framesID(213)), app.Videos.MediaID.Create(framesID(12)), app.Videos.StorageID.Create(storageG))); err != nil {
		t.Fatal(err)
	}
	if _, err := system.StorageObjects.Update(ctx, app.StorageObjects.ByID.Value(framesID(512)), app.StorageObjects.Update(app.StorageObjects.State.Set("PURGED"))); err != nil {
		t.Fatal(err)
	}
	oldFG, err := application.ForPrincipal(ctx, "delayed-old-FG")
	if err != nil {
		t.Fatal(err)
	}
	oldF, err := application.ForPrincipal(ctx, "delayed-old-F")
	if err != nil {
		t.Fatal(err)
	}
	newActor, err := application.ForPrincipal(ctx, "delayed-new")
	if err != nil {
		t.Fatal(err)
	}
	where := golem.Where(app.Videos.ID.Eq(framesID(212)))
	assertCount := func(label string, expected int64, query func() (int64, error)) {
		if count, err := query(); err != nil || count != expected {
			t.Fatalf("%s count=%d err=%v expected=%d", label, count, err, expected)
		}
	}
	assertCount("old FG Video before A", 1, func() (int64, error) { return oldFG.Videos.Count(ctx, where) })
	assertCount("old F Video before A", 1, func() (int64, error) { return oldF.Videos.Count(ctx, where) })
	assertCount("new title Video before A", 0, func() (int64, error) { return newActor.Videos.Count(ctx, where) })
	framesStartPublisherAndDrain(t, ctx, application, transport, audit)
	wait, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	oldFGStream, err := oldFG.Videos.Events(ctx, golem.EventWhere(app.Videos.ID.Eq(framesID(212))))
	if err != nil {
		t.Fatal(err)
	}
	defer oldFGStream.Close()
	oldFStream, err := oldF.Videos.Events(ctx, golem.EventWhere(app.Videos.ID.Eq(framesID(212))))
	if err != nil {
		t.Fatal(err)
	}
	defer oldFStream.Close()
	newStream, err := newActor.Videos.Events(ctx, golem.EventWhere(app.Videos.ID.Eq(framesID(212))))
	if err != nil {
		t.Fatal(err)
	}
	defer newStream.Close()
	gate := &framesPublishGate{model: audit.storageObject, entered: make(chan golem.CausationID, 1), forward: make(chan struct{}), finish: make(chan struct{})}
	if !transport.gate.CompareAndSwap(nil, gate) {
		t.Fatal("publisher gate already installed")
	}
	t.Cleanup(func() { transport.gate.Store(nil) })
	before := audit.completed.Load()
	// A records Media12's DENY-copy loss with Video212 still under Media12.
	if _, err := system.StorageObjects.Update(ctx, app.StorageObjects.ByID.Value(framesID(512)), app.StorageObjects.Update(app.StorageObjects.State.Set("MISSING"))); err != nil {
		t.Fatal(err)
	}
	var causationA golem.CausationID
	select {
	case causationA = <-gate.entered:
	case <-wait.Done():
		t.Fatal("native A did not reach the before-forward gate")
	}
	// B moves the observed sibling, not the changed copy's owning Video112.
	// It commits before any subscriber can see A's physical notice.
	if _, err := system.Videos.Update(ctx, app.Videos.ByID.Value(framesID(212)), app.Videos.Update(app.Videos.MediaID.Set(framesID(13)))); err != nil {
		t.Fatal(err)
	}
	assertCount("old FG current Video after B is read-denied", 0, func() (int64, error) { return oldFG.Videos.Count(ctx, where) })
	assertCount("old F current Video after B is readable", 1, func() (int64, error) { return oldF.Videos.Count(ctx, where) })
	assertCount("new title current Video after B is readable", 1, func() (int64, error) { return newActor.Videos.Count(ctx, where) })
	close(gate.forward)
	if published := framesAwaitPublication(t, wait, transport, audit.storageObject, golem.EventUpdated); published != causationA {
		t.Fatal("publisher forwarded a different causation")
	}
	framesExpectAttributedResync(t, ctx, "delayed A old FG recorded Media12 authority survives via G", oldFGStream)
	framesWait(t, wait, audit.wake, "three delayed-A dependency completions", func() bool { return audit.completed.Load() >= before+3 })
	framesExpectQuietAfterEvaluation(t, ctx, "delayed A old F revoked Media12 scope cannot borrow readable Media13", oldFStream)
	framesExpectQuietAfterEvaluation(t, ctx, "delayed A new title actor cannot borrow B's new route", newStream)
	// No B notice can race the quiet checks: one publisher worker is still
	// inside A's Publish call until finish. Close these streams before B runs.
	_ = oldFGStream.Close()
	_ = oldFStream.Close()
	_ = newStream.Close()
	close(gate.finish)
	framesAwaitDelivered(t, wait, application, audit, causationA)
	causationB := framesAwaitPublication(t, wait, transport, app.GolemGeneratedVideoDescriptor.Metadata().ModelID(), golem.EventUpdated)
	if causationB == causationA {
		t.Fatal("A and B shared a causation")
	}
	framesAwaitDelivered(t, wait, application, audit, causationB)
}

type framesReadClient[M any] interface {
	FindMany(context.Context, ...golem.ReadOption[M]) ([]golem.Row[M], error)
	Count(context.Context, ...golem.ReadOption[M]) (int64, error)
	FindUnique(context.Context, golem.UniqueSelectorValue[M], ...golem.ReadOption[M]) (golem.Row[M], error)
}

func framesAssertEventSourceVisibility[M any](t *testing.T, ctx context.Context, label string, client framesReadClient[M], filter golem.Predicate[M], selector golem.UniqueSelectorValue[M], visible bool) {
	t.Helper()
	expected := int64(0)
	if visible {
		expected = 1
	}
	if count, err := client.Count(ctx, golem.Where(filter)); err != nil || count != expected {
		t.Fatalf("%s Count=%d err=%v expected=%d", label, count, err, expected)
	}
	_, err := client.FindUnique(ctx, selector)
	if visible {
		if err != nil {
			t.Fatalf("%s FindUnique: %v", label, err)
		}
	} else if !errors.Is(err, golem.CodeNotFound) {
		t.Fatalf("%s hidden FindUnique=%v; expected NOT_FOUND", label, err)
	}
}

func framesAssertReadSurface[M any](t *testing.T, ctx context.Context, label string, client framesReadClient[M], id golem.ScalarColumn[M, golem.UUID], byID func(golem.UUID) golem.UniqueSelectorValue[M], projection golem.Projection[M], universe, expected []golem.UUID) {
	t.Helper()
	want := make(map[golem.UUID]bool, len(expected))
	for _, value := range expected {
		if want[value] {
			t.Fatalf("%s duplicate expected identity %s", label, value)
		}
		want[value] = true
	}
	rows, err := client.FindMany(ctx, projection)
	if err != nil {
		t.Fatalf("%s FindMany: %v", label, err)
	}
	seen := make(map[golem.UUID]bool, len(rows))
	for _, row := range rows {
		value, present := golem.Value(row, id).Get()
		if !present || seen[value] || !want[value] {
			t.Fatalf("%s unexpected/missing/duplicate identity %s present=%t", label, value, present)
		}
		seen[value] = true
	}
	if len(seen) != len(want) {
		t.Fatalf("%s identities=%v expected=%v", label, seen, expected)
	}
	count, err := client.Count(ctx)
	if err != nil || count != int64(len(expected)) {
		t.Fatalf("%s Count=%d err=%v expected=%d", label, count, err, len(expected))
	}
	for _, value := range universe {
		row, err := client.FindUnique(ctx, byID(value), projection)
		if !want[value] {
			if !errors.Is(err, golem.CodeNotFound) {
				t.Fatalf("%s denied FindUnique(%s)=%v; expected NOT_FOUND", label, value, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s allowed FindUnique(%s): %v", label, value, err)
		}
		actual, present := golem.Value(row, id).Get()
		if !present || actual != value {
			t.Fatalf("%s FindUnique returned %s present=%t for %s", label, actual, present, value)
		}
	}
}

func framesRequireWriteDenied(t *testing.T, operation string, err error) {
	t.Helper()
	if !errors.Is(err, golem.CodeForbidden) && !errors.Is(err, golem.CodeNotFound) {
		t.Fatalf("%s: expected authorization/existence-hiding refusal, got %v", operation, err)
	}
}

func framesRequireCreateRefusal(t *testing.T, operation string, err error, expected golem.ErrorCode) *golem.Error {
	t.Helper()
	var failure *golem.Error
	if !errors.As(err, &failure) || failure.Code != expected {
		t.Fatalf("%s: expected %s, got %v", operation, expected, err)
	}
	return failure
}

func framesRequireSameReferenceRefusal(t *testing.T, label string, unreadable, missing *golem.Error) {
	t.Helper()
	if unreadable == nil || missing == nil {
		t.Fatalf("%s: both reference refusals must have been observed", label)
	}
	if unreadable.Code != missing.Code || unreadable.Reason != missing.Reason || unreadable.Message != missing.Message || unreadable.Operation != missing.Operation || unreadable.Model != missing.Model || unreadable.Field != missing.Field {
		t.Fatalf("%s distinguished unreadable and missing references: unreadable=%s/%s/%s, missing=%s/%s/%s", label, unreadable.Code, unreadable.Reason, unreadable.Message, missing.Code, missing.Reason, missing.Message)
	}
}

func TestFramesPhysicalFactsPreserveSixModelReadsAndPrivateWrites(t *testing.T) {
	ctx := context.Background()
	var database *provider.Database
	var err error
	switch os.Getenv("GOLEM_OBJECT_PROVIDER") {
	case "sqlite":
		database, err = providersqlite.Open(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
	case "postgresql":
		database, err = providerpostgres.Open(ctx, providerpostgres.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB"), Pool: providerpostgres.PoolConfig{MaximumOpen: 4, MaximumIdle: 4}})
	default:
		t.Fatal("explicit reviewed SQLite/PostgreSQL provider is required")
	}
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	memory, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	transport := &framesEventTransport{EventTransport: memory, accepted: make(chan framesPublication, 8)}
	storageA, storageB, storageC, storageD, storageE := framesID(1001), framesID(1002), framesID(1003), framesID(1004), framesID(1005)
	profileOwn, profileForeign, profileNew, profileOutside := framesID(1101), framesID(1102), framesID(1103), framesID(1104)
	playlistOwn, playlistForeign, playlistShared, playlistNew, playlistOutside := framesID(1201), framesID(1202), framesID(1203), framesID(1204), framesID(1205)
	member := app.Actor{
		DomainStorageIDs: []golem.UUID{storageA, storageB}, GrantedStorageIDs: []golem.UUID{storageA},
		OpenStorageIDs: []golem.UUID{storageB}, RestrictedStorageIDs: []golem.UUID{storageC}, DeniedStorageIDs: []golem.UUID{storageE},
		AllowedMediaIDs: []golem.UUID{framesID(8)}, RemovedMediaIDs: []golem.UUID{framesID(11)},
		OwnedProfileIDs: []golem.UUID{profileOwn, profileNew}, VisiblePlaylistIDs: []golem.UUID{playlistOwn, playlistShared, playlistNew}, OwnedPlaylistIDs: []golem.UUID{playlistOwn, playlistNew},
	}
	owner, _ := framesActorSnapshot(member)
	owner.Owner = true
	guest := app.Actor{Guest: true, DomainStorageIDs: []golem.UUID{storageB}, OpenStorageIDs: []golem.UUID{storageB}, RestrictedStorageIDs: []golem.UUID{storageC}, DeniedStorageIDs: []golem.UUID{storageE}, RemovedMediaIDs: []golem.UUID{framesID(11)}}
	cap12, _ := framesActorSnapshot(member)
	cap12.Capped, cap12.MaxAge = true, 12
	cap18, _ := framesActorSnapshot(member)
	cap18.Capped, cap18.MaxAge = true, 18
	foreign := app.Actor{DomainStorageIDs: []golem.UUID{storageD}, GrantedStorageIDs: []golem.UUID{storageD}, DeniedStorageIDs: []golem.UUID{storageE}, RemovedMediaIDs: []golem.UUID{framesID(11)}, OwnedProfileIDs: []golem.UUID{profileForeign}, VisiblePlaylistIDs: []golem.UUID{playlistForeign}, OwnedPlaylistIDs: []golem.UUID{playlistForeign}}
	denyOpen, _ := framesActorSnapshot(member)
	denyOpen.DeniedMediaIDs = []golem.UUID{framesID(3)}
	explicitTitle := app.Actor{AllowedMediaIDs: []golem.UUID{framesID(4), framesID(8)}, RestrictedStorageIDs: []golem.UUID{storageC}, DeniedStorageIDs: []golem.UUID{storageE}, RemovedMediaIDs: []golem.UUID{framesID(11)}, OwnedProfileIDs: []golem.UUID{profileOwn}, VisiblePlaylistIDs: []golem.UUID{playlistOwn}, OwnedPlaylistIDs: []golem.UUID{playlistOwn}}
	actors := map[string]app.Actor{"owner": owner, "member": member, "guest": guest, "capped12": cap12, "capped18": cap18, "foreign": foreign, "deny-open": denyOpen, "explicit-title": explicitTitle}
	audit := &framesOperationAudit{storageObject: app.GolemGeneratedStorageObjectDescriptor.Metadata().ModelID(), initialPending: make(chan int64, 1), wake: make(chan struct{}, 1)}
	application, err := app.Open(ctx, app.Config[app.Principal]{Database: database, EventTransport: transport, Observer: audit,
		// Two selected hops plus the progress policy's four-hop authority
		// closure use the supported public limit without changing defaults.
		ReadLimits:          golemruntime.ReadLimits{MaxRelationDepth: 6},
		EventLimits:         events.Limits{PublisherConcurrency: 1, EvaluationConcurrency: 1, RetentionEvery: events.RetentionDisabled},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {},
		ResolvePrincipal: func(_ context.Context, principal app.Principal) (app.Actor, error) {
			actor, present := actors[string(principal)]
			if !present {
				return app.Actor{}, fmt.Errorf("unknown test principal")
			}
			return actor, nil
		}, SnapshotActor: framesActorSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	server, err := application.GraphQL(app.GraphQLConfig[app.Principal]{
		PrincipalFromContext: func(ctx context.Context) (app.Principal, bool) {
			principal, ok := ctx.Value(framesGraphQLPrincipalKey{}).(app.Principal)
			return principal, ok
		},
		ReportInternalError: func(_ context.Context, err error) { t.Errorf("trusted GraphQL failure: %v", err) },
	})
	if err != nil {
		t.Fatal(err)
	}
	system := application.System()
	allMedia := []uint32{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}
	allVideos := []uint32{1, 2, 3, 4, 5, 6, 7, 9, 10, 11, 12}
	stamp := time.Date(2026, 11, 13, 1, 0, 0, 0, time.UTC)
	for _, cell := range allMedia {
		values := []golem.CreateValue[app.Media]{app.Medias.ID.Create(framesID(cell)), app.Medias.Title.Create(fmt.Sprintf("cell-%d", cell))}
		if cell == 6 {
			values = append(values, app.Medias.AgeRating.CreateNull())
		} else {
			age := int64(12)
			if cell == 5 {
				age = 19
			}
			values = append(values, app.Medias.AgeRating.Create(age))
		}
		if cell == 5 {
			values = append(values, app.Medias.Adult.Create(true))
		}
		if cell == 10 {
			values = append(values, app.Medias.RetiredAt.Create(stamp))
		}
		if _, err := system.Medias.Create(ctx, app.Medias.Create(values...)); err != nil {
			t.Fatalf("seed Media %d: %v", cell, err)
		}
	}
	for _, cell := range allVideos {
		storage := storageA
		switch cell {
		case 2, 12:
			storage = storageD
		case 3, 4:
			storage = storageB
		}
		values := []golem.CreateValue[app.Video]{app.Videos.ID.Create(framesID(100 + cell)), app.Videos.MediaID.Create(framesID(cell)), app.Videos.StorageID.Create(storage)}
		if cell == 9 {
			values = append(values, app.Videos.RetiredAt.Create(stamp))
		}
		if _, err := system.Videos.Create(ctx, app.Videos.Create(values...)); err != nil {
			t.Fatalf("seed Video %d: %v", cell, err)
		}
		if _, err := system.Episodes.Create(ctx, app.Episodes.Create(app.Episodes.ID.Create(framesID(200+cell)), app.Episodes.ShowID.Create(framesID(cell)), app.Episodes.VideoID.Create(framesID(100+cell)), app.Episodes.Season.Create(1), app.Episodes.Number.Create(1))); err != nil {
			t.Fatalf("seed Episode %d: %v", cell, err)
		}
		for index, profile := range []golem.UUID{profileOwn, profileForeign} {
			base := uint32(300)
			if index == 1 {
				base = 600
			}
			if _, err := system.WatchProgresses.Create(ctx, app.WatchProgresses.Create(app.WatchProgresses.ID.Create(framesID(base+cell)), app.WatchProgresses.ProfileID.Create(profile), app.WatchProgresses.VideoID.Create(framesID(100+cell)), app.WatchProgresses.Percentage.Create(0.25))); err != nil {
				t.Fatalf("seed Progress %d/%d: %v", index, cell, err)
			}
		}
		for index, playlist := range []golem.UUID{playlistOwn, playlistForeign, playlistShared} {
			base := []uint32{400, 700, 800}[index]
			if _, err := system.PlaylistVideos.Create(ctx, app.PlaylistVideos.Create(app.PlaylistVideos.ID.Create(framesID(base+cell)), app.PlaylistVideos.PlaylistID.Create(playlist), app.PlaylistVideos.VideoID.Create(framesID(100+cell)), app.PlaylistVideos.Index.Create(int64(cell)))); err != nil {
				t.Fatalf("seed PlaylistVideo %d/%d: %v", index, cell, err)
			}
		}
	}
	for _, copy := range []struct {
		cell    uint32
		storage golem.UUID
		state   string
	}{
		{2, storageA, "MISSING"}, {4, storageC, "PRESENT"}, {7, storageE, "MISSING"}, {12, storageA, "PURGED"},
	} {
		if _, err := system.StorageObjects.Create(ctx, app.StorageObjects.Create(app.StorageObjects.ID.Create(framesID(500+copy.cell)), app.StorageObjects.VideoID.Create(framesID(100+copy.cell)), app.StorageObjects.StorageID.Create(copy.storage), app.StorageObjects.State.Create(copy.state))); err != nil {
			t.Fatalf("seed copy %d: %v", copy.cell, err)
		}
	}
	// Expected cells are fixed acceptance examples, not a second evaluator.
	for _, test := range []struct {
		principal     string
		media, videos []uint32
		profileBase   uint32
		playlistBases []uint32
	}{
		{"owner", []uint32{1, 2, 3, 4, 5, 6, 8, 9, 12}, []uint32{1, 2, 3, 4, 5, 6, 12}, 300, []uint32{400, 800}},
		{"member", []uint32{1, 2, 3, 5, 6, 8, 9}, []uint32{1, 2, 3, 5, 6}, 300, []uint32{400, 800}},
		{"guest", []uint32{3}, []uint32{3}, 0, nil},
		{"capped12", []uint32{1, 2, 3, 8, 9}, []uint32{1, 2, 3}, 300, []uint32{400, 800}},
		{"capped18", []uint32{1, 2, 3, 6, 8, 9}, []uint32{1, 2, 3, 6}, 300, []uint32{400, 800}},
		{"foreign", []uint32{2, 12}, []uint32{2, 12}, 600, []uint32{700}},
		{"deny-open", []uint32{1, 2, 5, 6, 8, 9}, []uint32{1, 2, 5, 6}, 300, []uint32{400, 800}},
		{"explicit-title", []uint32{4, 8}, []uint32{4}, 300, []uint32{400}},
	} {
		t.Run(test.principal, func(t *testing.T) {
			caller, err := application.ForPrincipal(ctx, app.Principal(test.principal))
			if err != nil {
				t.Fatal(err)
			}
			framesAssertReadSurface(t, ctx, "Media", caller.Medias, app.Medias.ID, app.Medias.ByID.Value, golem.Select[app.Media](app.Medias.ID), framesIDs(allMedia, 0), framesIDs(test.media, 0))
			framesAssertReadSurface(t, ctx, "Video", caller.Videos, app.Videos.ID, app.Videos.ByID.Value, golem.Select[app.Video](app.Videos.ID), framesIDs(allVideos, 100), framesIDs(test.videos, 100))
			framesAssertReadSurface(t, ctx, "Episode", caller.Episodes, app.Episodes.ID, app.Episodes.ByID.Value, golem.Select[app.Episode](app.Episodes.ID), framesIDs(allVideos, 200), framesIDs(test.videos, 200))
			var progress []golem.UUID
			if test.profileBase != 0 {
				progress = framesIDs(test.videos, test.profileBase)
			}
			framesAssertReadSurface(t, ctx, "WatchProgress", caller.WatchProgresses, app.WatchProgresses.ID, app.WatchProgresses.ByID.Value, golem.Select[app.WatchProgress](app.WatchProgresses.ID), framesIDs(allVideos, 300, 600), progress)
			framesAssertReadSurface(t, ctx, "PlaylistVideo", caller.PlaylistVideos, app.PlaylistVideos.ID, app.PlaylistVideos.ByID.Value, golem.Select[app.PlaylistVideo](app.PlaylistVideos.ID), framesIDs(allVideos, 400, 700, 800), framesIDs(test.videos, test.playlistBases...))
			data := framesGraphQLData(t, ctx, server.Handler(), app.Principal(test.principal), "{ medias { id } videos { id } episodes { id } watchProgresses { id } playlistVideos { id } }")
			framesGraphQLIDs(t, "HTTP Media", data["medias"], framesIDs(test.media, 0))
			framesGraphQLIDs(t, "HTTP Video", data["videos"], framesIDs(test.videos, 100))
			framesGraphQLIDs(t, "HTTP Episode", data["episodes"], framesIDs(test.videos, 200))
			framesGraphQLIDs(t, "HTTP WatchProgress", data["watchProgresses"], progress)
			framesGraphQLIDs(t, "HTTP PlaylistVideo", data["playlistVideos"], framesIDs(test.videos, test.playlistBases...))
			if rows, err := caller.StorageObjects.FindMany(ctx); len(rows) != 0 || !errors.Is(err, golem.CodeForbidden) {
				t.Fatalf("hidden StorageObject rows=%d err=%v", len(rows), err)
			}
			if count, err := caller.StorageObjects.Count(ctx); count != 0 || !errors.Is(err, golem.CodeForbidden) {
				t.Fatalf("hidden StorageObject count=%d err=%v", count, err)
			}
			if _, err := caller.StorageObjects.FindUnique(ctx, app.StorageObjects.ByID.Value(framesID(502))); !errors.Is(err, golem.CodeForbidden) {
				t.Fatalf("hidden StorageObject unique=%v", err)
			}
		})
	}
	t.Run("GraphQL-selections", func(t *testing.T) { framesGraphQLSelections(t, ctx, server.Handler(), application) })
	caller, err := application.ForPrincipal(ctx, "member")
	if err != nil {
		t.Fatal(err)
	}
	admin, err := application.ForPrincipal(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	visitor, err := application.ForPrincipal(ctx, "guest")
	if err != nil {
		t.Fatal(err)
	}
	// Ordinary Golem writes retain root read reach even with a wider update
	// grant. Frames administration deliberately uses its audited owner escape.
	beforeEscapes := audit.escapes.Load()
	if count, err := admin.Medias.UpdateMany(ctx, app.Medias.ID.Eq(framesID(7)), app.Medias.UpdateMany(app.Medias.Title.Set("ordinary-owner-leak"))); err != nil || count != 0 {
		t.Fatalf("ordinary owner write ignored root read reach: count=%d err=%v", count, err)
	}
	persisted, err := system.Medias.FindUnique(ctx, app.Medias.ByID.Value(framesID(7)))
	if err != nil {
		t.Fatal(err)
	}
	if title, present := golem.Value(persisted, app.Medias.Title).Get(); !present || title != "cell-7" || audit.escapes.Load() != beforeEscapes {
		t.Fatalf("ordinary read-gated owner write changed state or escaped: title=%q present=%t escapes=%d", title, present, audit.escapes.Load()-beforeEscapes)
	}
	runOwnerRetitle := func(operationContext context.Context, caller *app.Caller[app.Principal], title string) (int64, error) {
		var count int64
		err := caller.Transaction(operationContext, func(transaction *app.CallerTx[app.Principal]) error {
			var err error
			count, err = app.Run(operationContext, transaction, app.OwnerRetitle, app.OwnerRetitleArgs{ID: framesID(7), Title: title})
			return err
		})
		return count, err
	}
	readAdminTitle := func(t *testing.T) string {
		t.Helper()
		row, err := system.Medias.FindUnique(ctx, app.Medias.ByID.Value(framesID(7)))
		if err != nil {
			t.Fatal(err)
		}
		title, present := golem.Value(row, app.Medias.Title).Get()
		if !present {
			t.Fatal("administrative title missing from system read")
		}
		return title
	}
	resetAdminTitle := func(t *testing.T, title string) {
		t.Helper()
		if count, err := system.Medias.UpdateMany(ctx, app.Medias.ID.Eq(framesID(7)), app.Medias.UpdateMany(app.Medias.Title.Set(title))); err != nil || count != 1 {
			t.Fatalf("reset operation target count=%d err=%v", count, err)
		}
	}
	// These are independent subtests so a missing owner actor does not prevent
	// the forged-owner privilege-escalation attempt from executing on red.
	t.Run("operation-owner-without-ambient-actor", func(t *testing.T) {
		resetAdminTitle(t, "owner-before")
		before := audit.escapes.Load()
		count, err := runOwnerRetitle(ctx, admin, "owner-authorized")
		if err != nil || count != 1 || audit.escapes.Load() != before+1 {
			t.Errorf("owner operation count=%d err=%v escape delta=%d", count, err, audit.escapes.Load()-before)
		}
		if title := readAdminTitle(t); title != "owner-authorized" {
			t.Errorf("owner operation persisted %q", title)
		}
	})
	for _, denied := range []struct {
		name   string
		caller *app.Caller[app.Principal]
	}{{"member", caller}, {"Guest", visitor}} {
		t.Run("operation-"+denied.name+"-without-ambient-actor", func(t *testing.T) {
			resetAdminTitle(t, "non-owner-before")
			before := audit.escapes.Load()
			count, err := runOwnerRetitle(ctx, denied.caller, "non-owner-leak")
			if count != 0 || !errors.Is(err, golem.CodeForbidden) || audit.escapes.Load() != before {
				t.Errorf("non-owner operation count=%d err=%v escape delta=%d", count, err, audit.escapes.Load()-before)
			}
			if title := readAdminTitle(t); title != "non-owner-before" {
				t.Errorf("denied operation changed title to %q", title)
			}
		})
	}
	t.Run("operation-member-rejects-forged-ambient-owner", func(t *testing.T) {
		resetAdminTitle(t, "forgery-before")
		before := audit.escapes.Load()
		forged := golem.RuntimeContextWithActor(ctx, app.Actor{Owner: true})
		count, err := runOwnerRetitle(forged, caller, "forged-owner-leak")
		if count != 0 || !errors.Is(err, golem.CodeForbidden) || audit.escapes.Load() != before {
			t.Errorf("forged ambient owner authorized member: count=%d err=%v escape delta=%d", count, err, audit.escapes.Load()-before)
		}
		if title := readAdminTitle(t); title != "forgery-before" {
			t.Errorf("forged owner persisted unauthorized title %q", title)
		}
	})
	t.Run("operation-owner-overrides-forged-ambient-member", func(t *testing.T) {
		resetAdminTitle(t, "owner-forged-member-before")
		before := audit.escapes.Load()
		forged := golem.RuntimeContextWithActor(ctx, app.Actor{Owner: false})
		count, err := runOwnerRetitle(forged, admin, "real-owner-authorized")
		if err != nil || count != 1 || audit.escapes.Load() != before+1 {
			t.Errorf("forged member replaced owner snapshot: count=%d err=%v escape delta=%d", count, err, audit.escapes.Load()-before)
		}
		if title := readAdminTitle(t); title != "real-owner-authorized" {
			t.Errorf("real owner persisted %q", title)
		}
	})
	t.Run("operation-retained-snapshot-and-fresh-principal", func(t *testing.T) {
		resetAdminTitle(t, "snapshot-before")
		actors["snapshot"] = owner
		defer delete(actors, "snapshot")
		retained, err := application.ForPrincipal(ctx, "snapshot")
		if err != nil {
			t.Fatal(err)
		}
		before := audit.escapes.Load()
		var count int64
		err = retained.Transaction(ctx, func(transaction *app.CallerTx[app.Principal]) error {
			// Change the external resolver after the owner transaction has been
			// admitted; its actor snapshot must remain transaction-owned.
			actors["snapshot"] = member
			var err error
			count, err = app.Run(ctx, transaction, app.OwnerRetitle, app.OwnerRetitleArgs{ID: framesID(7), Title: "retained-transaction-owner"})
			return err
		})
		if err != nil || count != 1 || audit.escapes.Load() != before+1 {
			t.Errorf("admitted transaction lost actor snapshot: count=%d err=%v escape delta=%d", count, err, audit.escapes.Load()-before)
		}
		if title := readAdminTitle(t); title != "retained-transaction-owner" {
			t.Errorf("retained transaction persisted %q", title)
		}
		before = audit.escapes.Load()
		count, err = runOwnerRetitle(ctx, retained, "retained-caller-owner")
		if err != nil || count != 1 || audit.escapes.Load() != before+1 {
			t.Errorf("retained caller lost actor snapshot: count=%d err=%v escape delta=%d", count, err, audit.escapes.Load()-before)
		}
		if title := readAdminTitle(t); title != "retained-caller-owner" {
			t.Errorf("retained caller persisted %q", title)
		}
		fresh, err := application.ForPrincipal(ctx, "snapshot")
		if err != nil {
			t.Fatal(err)
		}
		before, priorTitle := audit.escapes.Load(), readAdminTitle(t)
		forged := golem.RuntimeContextWithActor(ctx, app.Actor{Owner: true})
		count, err = runOwnerRetitle(forged, fresh, "fresh-member-leak")
		if count != 0 || !errors.Is(err, golem.CodeForbidden) || audit.escapes.Load() != before {
			t.Errorf("fresh principal reused owner/ambient authority: count=%d err=%v escape delta=%d", count, err, audit.escapes.Load()-before)
		}
		if title := readAdminTitle(t); title != priorTitle {
			t.Errorf("fresh denied operation changed %q to %q", priorTitle, title)
		}
	})
	if _, err := admin.Medias.FindUnique(ctx, app.Medias.ByID.Value(framesID(7))); !errors.Is(err, golem.CodeNotFound) {
		t.Fatalf("administrative write bypassed read DENY: %v", err)
	}
	_, err = caller.Medias.UpdateMany(ctx, app.Medias.ID.Eq(framesID(1)), app.Medias.UpdateMany(app.Medias.Title.Set("member-leak")))
	framesRequireWriteDenied(t, "member media write", err)
	_, err = visitor.Medias.UpdateMany(ctx, app.Medias.ID.Eq(framesID(3)), app.Medias.UpdateMany(app.Medias.Title.Set("open-leak")))
	framesRequireWriteDenied(t, "Guest open-baseline write", err)
	for _, cell := range []uint32{1, 3} {
		row, err := system.Medias.FindUnique(ctx, app.Medias.ByID.Value(framesID(cell)))
		if err != nil {
			t.Fatal(err)
		}
		if title, present := golem.Value(row, app.Medias.Title).Get(); !present || title != fmt.Sprintf("cell-%d", cell) {
			t.Fatalf("denied media write changed %d: %q", cell, title)
		}
	}
	if _, err := caller.WatchProgresses.Create(ctx, app.WatchProgresses.Create(app.WatchProgresses.ID.Create(framesID(900)), app.WatchProgresses.ProfileID.Create(profileNew), app.WatchProgresses.VideoID.Create(framesID(101)), app.WatchProgresses.Percentage.Create(0.5))); err != nil {
		t.Fatalf("own progress create: %v", err)
	}
	if _, err := caller.WatchProgresses.Update(ctx, app.WatchProgresses.ByID.Value(framesID(900)), app.WatchProgresses.Update(app.WatchProgresses.Percentage.Set(0.75))); err != nil {
		t.Fatalf("own progress update: %v", err)
	}
	progressRow, err := caller.WatchProgresses.FindUnique(ctx, app.WatchProgresses.ByID.Value(framesID(900)))
	if err != nil {
		t.Fatal(err)
	}
	if value, present := golem.Value(progressRow, app.WatchProgresses.Percentage).Get(); !present || value != 0.75 {
		t.Fatalf("own progress value=%v present=%t", value, present)
	}
	if _, err := caller.WatchProgresses.Delete(ctx, app.WatchProgresses.ByID.Value(framesID(900))); err != nil {
		t.Fatalf("own progress delete: %v", err)
	}
	if _, err := system.WatchProgresses.FindUnique(ctx, app.WatchProgresses.ByID.Value(framesID(900))); !errors.Is(err, golem.CodeNotFound) {
		t.Fatalf("own progress survived delete: %v", err)
	}
	var unreadableProgress, missingProgress *golem.Error
	for _, test := range []struct {
		name    string
		id      uint32
		profile golem.UUID
		video   uint32
		code    golem.ErrorCode
	}{
		{"foreign-profile", 901, profileOutside, 101, golem.CodeForbidden},
		{"unreadable-video", 902, profileNew, 104, golem.CodeBadUserInput},
		{"missing-video", 903, profileNew, 1999, golem.CodeBadUserInput},
	} {
		_, err := caller.WatchProgresses.Create(ctx, app.WatchProgresses.Create(app.WatchProgresses.ID.Create(framesID(test.id)), app.WatchProgresses.ProfileID.Create(test.profile), app.WatchProgresses.VideoID.Create(framesID(test.video))))
		failure := framesRequireCreateRefusal(t, "progress create "+test.name, err, test.code)
		if test.name == "unreadable-video" {
			unreadableProgress = failure
		}
		if test.name == "missing-video" {
			missingProgress = failure
		}
		if _, err := system.WatchProgresses.FindUnique(ctx, app.WatchProgresses.ByID.Value(framesID(test.id))); !errors.Is(err, golem.CodeNotFound) {
			t.Fatalf("denied progress create %s persisted: %v", test.name, err)
		}
	}
	framesRequireSameReferenceRefusal(t, "progress create", unreadableProgress, missingProgress)
	for _, rowID := range []uint32{601, 304} {
		_, err := caller.WatchProgresses.Update(ctx, app.WatchProgresses.ByID.Value(framesID(rowID)), app.WatchProgresses.Update(app.WatchProgresses.Percentage.Set(0.99)))
		framesRequireWriteDenied(t, "foreign/unreadable progress update", err)
		_, err = caller.WatchProgresses.Delete(ctx, app.WatchProgresses.ByID.Value(framesID(rowID)))
		framesRequireWriteDenied(t, "foreign/unreadable progress delete", err)
		row, err := system.WatchProgresses.FindUnique(ctx, app.WatchProgresses.ByID.Value(framesID(rowID)))
		if err != nil {
			t.Fatalf("denied progress delete lost source: %v", err)
		}
		if value, present := golem.Value(row, app.WatchProgresses.Percentage).Get(); !present || value != 0.25 {
			t.Fatalf("denied progress update persisted: %v", value)
		}
	}
	if _, err := caller.PlaylistVideos.Create(ctx, app.PlaylistVideos.Create(app.PlaylistVideos.ID.Create(framesID(910)), app.PlaylistVideos.PlaylistID.Create(playlistNew), app.PlaylistVideos.VideoID.Create(framesID(101)), app.PlaylistVideos.Index.Create(0))); err != nil {
		t.Fatalf("own playlist create: %v", err)
	}
	if _, err := caller.PlaylistVideos.Update(ctx, app.PlaylistVideos.ByID.Value(framesID(910)), app.PlaylistVideos.Update(app.PlaylistVideos.Index.Set(3))); err != nil {
		t.Fatalf("own playlist update: %v", err)
	}
	playlistRow, err := caller.PlaylistVideos.FindUnique(ctx, app.PlaylistVideos.ByID.Value(framesID(910)))
	if err != nil {
		t.Fatal(err)
	}
	if value, present := golem.Value(playlistRow, app.PlaylistVideos.Index).Get(); !present || value != 3 {
		t.Fatalf("own playlist position=%v present=%t", value, present)
	}
	if _, err := caller.PlaylistVideos.Delete(ctx, app.PlaylistVideos.ByID.Value(framesID(910))); err != nil {
		t.Fatalf("own playlist delete: %v", err)
	}
	if _, err := system.PlaylistVideos.FindUnique(ctx, app.PlaylistVideos.ByID.Value(framesID(910))); !errors.Is(err, golem.CodeNotFound) {
		t.Fatalf("own playlist survived delete: %v", err)
	}
	var unreadablePlaylist, missingPlaylist *golem.Error
	for _, test := range []struct {
		name     string
		id       uint32
		playlist golem.UUID
		video    uint32
		code     golem.ErrorCode
	}{
		{"foreign-playlist", 911, playlistOutside, 101, golem.CodeForbidden},
		{"unreadable-video", 912, playlistNew, 104, golem.CodeBadUserInput},
		{"missing-video", 913, playlistNew, 1999, golem.CodeBadUserInput},
	} {
		_, err := caller.PlaylistVideos.Create(ctx, app.PlaylistVideos.Create(app.PlaylistVideos.ID.Create(framesID(test.id)), app.PlaylistVideos.PlaylistID.Create(test.playlist), app.PlaylistVideos.VideoID.Create(framesID(test.video))))
		failure := framesRequireCreateRefusal(t, "playlist create "+test.name, err, test.code)
		if test.name == "unreadable-video" {
			unreadablePlaylist = failure
		}
		if test.name == "missing-video" {
			missingPlaylist = failure
		}
		if _, err := system.PlaylistVideos.FindUnique(ctx, app.PlaylistVideos.ByID.Value(framesID(test.id))); !errors.Is(err, golem.CodeNotFound) {
			t.Fatalf("denied playlist create %s persisted: %v", test.name, err)
		}
	}
	framesRequireSameReferenceRefusal(t, "playlist create", unreadablePlaylist, missingPlaylist)
	for _, rowID := range []uint32{701, 801, 404} {
		_, err := caller.PlaylistVideos.Update(ctx, app.PlaylistVideos.ByID.Value(framesID(rowID)), app.PlaylistVideos.Update(app.PlaylistVideos.Index.Set(99)))
		framesRequireWriteDenied(t, "foreign/shared/unreadable playlist update", err)
		_, err = caller.PlaylistVideos.Delete(ctx, app.PlaylistVideos.ByID.Value(framesID(rowID)))
		framesRequireWriteDenied(t, "foreign/shared/unreadable playlist delete", err)
		row, err := system.PlaylistVideos.FindUnique(ctx, app.PlaylistVideos.ByID.Value(framesID(rowID)))
		if err != nil {
			t.Fatalf("denied playlist delete lost source: %v", err)
		}
		if value, present := golem.Value(row, app.PlaylistVideos.Index).Get(); !present || value != int64(rowID%100) {
			t.Fatalf("denied playlist update persisted: %v", value)
		}
	}
	// ADULTS admits an adult-flagged 18-rated title; lower caps still refuse it.
	if _, err := system.Medias.UpdateMany(ctx, app.Medias.ID.Eq(framesID(5)), app.Medias.UpdateMany(app.Medias.AgeRating.Set(18))); err != nil {
		t.Fatal(err)
	}
	adult18, err := application.ForPrincipal(ctx, "capped18")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adult18.Medias.FindUnique(ctx, app.Medias.ByID.Value(framesID(5))); err != nil {
		t.Fatalf("ADULTS incorrectly excluded adult flag: %v", err)
	}
	adult12, err := application.ForPrincipal(ctx, "capped12")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adult12.Medias.FindUnique(ctx, app.Medias.ByID.Value(framesID(5))); !errors.Is(err, golem.CodeNotFound) {
		t.Fatalf("lower cap admitted adult title: %v", err)
	}
	t.Run("native-fact-authority-event-gain", func(t *testing.T) { framesNativeFactEventGain(t, ctx, application, transport, audit, actors) })
	t.Run("native-fact-authority-delayed-reparent", func(t *testing.T) {
		framesNativeFactEventDelayedReparent(t, ctx, application, transport, audit, actors)
	})
}
`

// Explicit characterization only: no performance threshold or production/NAS claim.
// Allow generation plus execution with go test -timeout=40m on a busy host.
func TestFreshGeneratedFrames25000VideoCharacterization(t *testing.T) {
	if os.Getenv("GOLEM_CHARACTERIZE_FRAMES_25K") != "1" {
		t.Skip("opt in with GOLEM_CHARACTERIZE_FRAMES_25K=1")
	}
	if os.Getenv("GOLEM_POLICY_HISTORY_WRITER_REHEARSAL") != "4" {
		t.Fatal("25k named-fact characterization requires explicit writer4 rehearsal")
	}
	if strings.Count(framesFactsConsumerTest, "\t\"time\"") != 1 {
		t.Fatal("frames consumer import anchor changed")
	}
	consumer := strings.Replace(framesFactsConsumerTest, "\t\"time\"", "\t\"time\"\n goruntime \"runtime\"\n \"sort\"\n \"github.com/eleven-am/golem/go/queryplan\"", 1) + frames25000ConsumerTest
	indexMode := os.Getenv("GOLEM_CHARACTERIZE_FRAMES_25K_INDEXES")
	if indexMode != "" && indexMode != "relations" {
		t.Fatal("GOLEM_CHARACTERIZE_FRAMES_25K_INDEXES must be empty or relations")
	}
	var rewrite func(string) string
	if indexMode == "relations" {
		// Declare ordinary application indexes through the compiler. Keep the
		// rows, policies, query and mutation budgets identical to the baseline.
		rewrite = framesCharacterizationIndexes(t)
	}
	runGeneratedDTOConsumerArtifacts(t, "framesfacts", rewrite, consumer, generatedDTOExecution{writer4Only: true, sqliteDirectory: os.Getenv("GOLEM_CHARACTERIZE_FRAMES_SQLITE_DIRECTORY"), coordinate: func(t *testing.T, artifacts generatedDTOArtifacts) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, artifacts.Writer4, "-test.v", "-test.count=1", "-test.timeout=11m", "-test.run=^TestFrames25000VideoQueryCharacterization$")
		command.Dir = artifacts.Directory
		command.Env = append(append([]string(nil), artifacts.Environment...), "GOLEM_OBJECT_PROVIDER="+string(artifacts.Provider), "GOLEM_OBJECT_DB="+artifacts.DSN)
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			t.Fatalf("25k characterization (%s): %v", artifacts.Provider, err)
		}
	}}, compilerir.SQLite, compilerir.PostgreSQL)
}

const frames25000ConsumerTest = `
// This opt-in query characterization uses real writer4 public mutations. It is
// not a NAS measurement, a production baseline, or a latency pass/fail budget.
func TestFrames25000VideoQueryCharacterization(t *testing.T) {
	if os.Getenv("GOLEM_CHARACTERIZE_FRAMES_25K") != "1" || os.Getenv("GOLEM_POLICY_HISTORY_WRITER_REHEARSAL") != "4" {
		t.Fatal("explicit25k writer4 characterization opt-in required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var database *provider.Database
	var err error
	selected := os.Getenv("GOLEM_OBJECT_PROVIDER")
	profile, systemNamespace := "", ""
	filesystem := "disposable local temp path; mount type and storage hardware not measured; NOT NAS"
	switch selected {
	case "sqlite":
		// The owner provisions only a fresh scratch database. The override moves
		// its data, not the generated source or compiler temporary files.
		database, err = providersqlite.OpenNetworkFilesystem(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
		profile, systemNamespace = "sqlite-network-filesystem-policy-on-local-temp-NOT-NAS", "main"
		if os.Getenv("GOLEM_CHARACTERIZE_FRAMES_SQLITE_DIRECTORY") != "" {
			profile = "sqlite-network-filesystem-policy-on-owned-external-scratch"
			filesystem = "explicit scratch parent; verify the physical mount separately; storage hardware not measured"
		}
	case "postgresql":
		database, err = providerpostgres.Open(ctx, providerpostgres.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB"), Pool: providerpostgres.PoolConfig{MaximumOpen: 4, MaximumIdle: 4}})
		profile, systemNamespace = "postgresql-private-owned-root", "_golem"
		filesystem = "private PostgreSQL fixture; storage hardware not measured"
	default:
		t.Fatal("explicit characterization provider required")
	}
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	storageA, storageB, storageC, storageD, storageE := framesID(1001), framesID(1002), framesID(1003), framesID(1004), framesID(1005)
	member := app.Actor{DomainStorageIDs: []golem.UUID{storageA, storageB}, GrantedStorageIDs: []golem.UUID{storageA}, OpenStorageIDs: []golem.UUID{storageB}, RestrictedStorageIDs: []golem.UUID{storageC}, DeniedStorageIDs: []golem.UUID{storageE}}
	application, err := app.Open(ctx, app.Config[app.Principal]{Database: database, EventTransport: transport,
		// This corpus has one video per media and at most one copy per video.
		// Bound private policy hydration to that known topology so Explain can
		// truthfully report a finite worst-case execution statement count.
		ReadLimits: golemruntime.ReadLimits{MaxRelationDepth: 6, MaxRelationFanout: 1},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) {
			t.Error("characterization unexpectedly operated on durable events")
		},
		ResolvePrincipal: func(_ context.Context, p app.Principal) (app.Actor, error) {
			if p != "member" {
				return app.Actor{}, fmt.Errorf("unknown principal")
			}
			return member, nil
		}, SnapshotActor: framesActorSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	const videos = 25000
	caller, err := application.ForPrincipal(ctx, "member")
	if err != nil {
		t.Fatal("named-fact caller admission", err)
	}
	options := []golem.ReadOption[app.Video]{golem.Select[app.Video](app.Videos.ID, app.Videos.MediaID, app.Videos.StorageID), golem.OrderBy(app.Videos.ID.Asc()), golem.Take[app.Video](100)}
	// Explain has its own diagnostic budget. Preserve a bounded refusal without
	// confusing it with failure to execute the authorized workload.
	if _, err := caller.Videos.ExplainFindMany(ctx, options...); err != nil {
		if code, ok := queryplan.CodeOf(err); !ok || code != queryplan.ErrorTooComplex {
			t.Fatalf("empty-database plan preflight: %v", err)
		}
		t.Logf("plan preflight diagnostic limit: %v", err)
	}
	t.Log("seeding 25,000 independent video graphs")
	// Twenty-five independent graphs per transaction: <=65 directly touched models and
	// facts, with capture closure bounded by those graphs. Runtime hard limits
	// remain active; inspect the actual emitted group bounds below as evidence.
	const graphsPerBatch = 25
	system := application.System()
	expected := make([]golem.UUID, 0, 10000)
	outbox := "\"" + systemNamespace + "\".\"_golem_outbox\""
	seedStarted := time.Now()
	for base := uint32(0); base < videos; base += graphsPerBatch {
		if err := system.Transaction(ctx, func(tx *app.SystemTx[app.Principal]) error {
			for index := base; index < base+graphsPerBatch && index < videos; index++ {
				mediaID, videoID := framesID(100000+index), framesID(200000+index)
				if _, err := tx.Medias.Create(ctx, app.Medias.Create(app.Medias.ID.Create(mediaID), app.Medias.Title.Create(fmt.Sprintf("video-%05d", index)), app.Medias.AgeRating.Create(12))); err != nil {
					return err
				}
				storage := storageA
				switch index % 5 {
				case 1, 3:
					storage = storageD
				case 2:
					storage = storageB
				}
				if _, err := tx.Videos.Create(ctx, app.Videos.Create(app.Videos.ID.Create(videoID), app.Videos.MediaID.Create(mediaID), app.Videos.StorageID.Create(storage))); err != nil {
					return err
				}
				var copyStorage golem.UUID
				switch index % 5 {
				case 1:
					copyStorage = storageA
				case 2:
					copyStorage = storageC
				case 4:
					copyStorage = storageE
				}
				if copyStorage != (golem.UUID{}) {
					if _, err := tx.StorageObjects.Create(ctx, app.StorageObjects.Create(app.StorageObjects.ID.Create(framesID(300000+index)), app.StorageObjects.VideoID.Create(videoID), app.StorageObjects.StorageID.Create(copyStorage), app.StorageObjects.State.Create("PRESENT"))); err != nil {
						return err
					}
				}
			}
			return nil
		}); err != nil {
			t.Fatalf("seed batch at%d failed existing writer bounds: %v", base, err)
		}
		if base == 0 {
			var firstFacts, firstLegacy, firstBytes int64
			if err := database.UnsafeSQLX().GetContext(ctx, &firstFacts, "SELECT count(*) FROM "+outbox); err != nil {
				t.Fatal(err)
			}
			if err := database.UnsafeSQLX().GetContext(ctx, &firstLegacy, "SELECT count(*) FROM "+outbox+" WHERE fact_version<>4"); err != nil {
				t.Fatal(err)
			}
			if err := database.UnsafeSQLX().GetContext(ctx, &firstBytes, "SELECT sum(coalesce(length(before_identity),0)+coalesce(length(after_identity),0)+coalesce(length(metadata),0)+coalesce(length(delete_snapshot),0)) FROM "+outbox); err != nil {
				t.Fatal(err)
			}
			if firstFacts != 65 || firstLegacy != 0 || firstFacts > golemruntime.MaxMutationFacts || firstBytes < 1 || firstBytes > golemruntime.MaxMutationOutboxBytes {
				t.Fatalf("pilot batch facts=%d legacy=%d evidencebytes=%d violates actual writer4 bounds", firstFacts, firstLegacy, firstBytes)
			}
		}
		for index := base; index < base+graphsPerBatch && index < videos; index++ {
			if index%5 == 0 || index%5 == 1 {
				expected = append(expected, framesID(200000+index))
			}
		}
	}
	seedElapsed := time.Since(seedStarted)
	t.Logf("25k seed complete in %s; relation indexes=%q", seedElapsed, os.Getenv("GOLEM_CHARACTERIZE_FRAMES_25K_INDEXES"))
	if count, err := system.Videos.Count(ctx); err != nil || count != videos {
		t.Fatalf("video cardinality=%d want25000 err=%v", count, err)
	}
	if count, err := system.Medias.Count(ctx); err != nil || count != videos {
		t.Fatalf("independent media cardinality=%d err=%v", count, err)
	}
	if count, err := system.StorageObjects.Count(ctx); err != nil || count != 15000 {
		t.Fatalf("copy cardinality=%d err=%v", count, err)
	}
	for _, topology := range []struct{ table, parent string }{{"videos", "media_id"}, {"storage_objects", "video_id"}} {
		var maximum int
		if err := database.UnsafeSQLX().GetContext(ctx, &maximum, "SELECT max(n) FROM (SELECT count(*) AS n FROM \""+topology.table+"\" GROUP BY \""+topology.parent+"\") topology"); err != nil || maximum != 1 {
			t.Fatalf("fixture relation fanout %s.%s=%d want1 err=%v", topology.table, topology.parent, maximum, err)
		}
	}

	var facts, legacy, maxGroup, maxBytes int64
	if err := database.UnsafeSQLX().GetContext(ctx, &facts, "SELECT count(*) FROM "+outbox+" WHERE fact_version=4"); err != nil {
		t.Fatal(err)
	}
	if err := database.UnsafeSQLX().GetContext(ctx, &legacy, "SELECT count(*) FROM "+outbox+" WHERE fact_version<>4"); err != nil {
		t.Fatal(err)
	}
	if facts != 65000 || legacy != 0 {
		t.Fatalf("actual writer4 evidence facts=%d otherformats=%d", facts, legacy)
	}
	if err := database.UnsafeSQLX().GetContext(ctx, &maxGroup, "SELECT max(n) FROM (SELECT count(*) AS n FROM "+outbox+" GROUP BY causation_id) groups"); err != nil {
		t.Fatal(err)
	}
	if err := database.UnsafeSQLX().GetContext(ctx, &maxBytes, "SELECT max(n) FROM (SELECT sum(coalesce(length(before_identity),0)+coalesce(length(after_identity),0)+coalesce(length(metadata),0)+coalesce(length(delete_snapshot),0)) AS n FROM "+outbox+" GROUP BY causation_id) groups"); err != nil {
		t.Fatal(err)
	}
	if maxGroup < 1 || maxGroup > golemruntime.MaxMutationFacts || maxBytes < 1 || maxBytes > golemruntime.MaxMutationOutboxBytes {
		t.Fatalf("actual causation bounds facts=%d bytes=%d", maxGroup, maxBytes)
	}
	query := func() time.Duration {
		started := time.Now()
		rows, err := caller.Videos.FindMany(ctx, options...)
		elapsed := time.Since(started)
		if err != nil || len(rows) != 100 {
			t.Fatalf("authorized page rows=%d err=%v", len(rows), err)
		}
		for index, row := range rows {
			id, present := golem.Value(row, app.Videos.ID).Get()
			if !present || id != expected[index] {
				t.Fatalf("unauthorized/incorrect page row at%d", index)
			}
		}
		return elapsed
	}
	// First measured FindMany follows preflight Explain and seeding. OS, SQLite
	// and policy caches are not flushed: this must never be labeled a cold query.
	first := query()
	t.Logf("first authorized page took %s", first)
	report, err := caller.Videos.ExplainFindMany(ctx, options...)
	planStatus := "available"
	if err != nil {
		code, ok := queryplan.CodeOf(err)
		if !ok || code != queryplan.ErrorTooComplex {
			t.Fatal(err)
		}
		planStatus = string(code)
		t.Logf("complete public plan report unavailable: %s; continuing workload measurement", planStatus)
	}
	plan := map[string]int{}
	visited := 0
	var walk func(queryplan.Node)
	walk = func(node queryplan.Node) {
		if visited >= 4096 {
			t.Fatal("unexpectedly oversized logical plan")
		}
		visited++
		plan[string(node.Kind())+"/"+string(node.Access())]++
		for _, child := range node.Children() {
			walk(child)
		}
	}
	for _, statement := range report.Statements() {
		walk(statement.Root())
	}
	if planStatus == "available" {
		t.Logf("authorized page plan nodes=%v warnings=%v", plan, report.Warnings())
	}
	countStarted := time.Now()
	if count, err := caller.Videos.Count(ctx); err != nil || count != int64(len(expected)) {
		t.Fatalf("authorized count=%d want%d elapsed=%s context=%v err=%v", count, len(expected), time.Since(countStarted), ctx.Err(), err)
	}
	countElapsed := time.Since(countStarted)
	t.Logf("authorized count took %s", countElapsed)
	all, err := caller.Videos.FindMany(ctx, golem.Select[app.Video](app.Videos.ID), golem.OrderBy(app.Videos.ID.Asc()), golem.Take[app.Video](videos))
	if err != nil || len(all) != len(expected) {
		t.Fatalf("authorized full set length=%d err=%v", len(all), err)
	}
	for index, row := range all {
		id, present := golem.Value(row, app.Videos.ID).Get()
		if !present || id != expected[index] {
			t.Fatalf("authorized exact partition differs at%d", index)
		}
	}
	t.Log("authorized full-set equality passed; collecting warm page samples")
	for warmup := 0; warmup < 10; warmup++ {
		query()
	}
	var beforeMemory, afterMemory goruntime.MemStats
	goruntime.ReadMemStats(&beforeMemory)
	samples := make([]time.Duration, 200)
	for index := range samples {
		samples[index] = query()
	}
	goruntime.ReadMemStats(&afterMemory)
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	// Empirical nearest-rank percentiles, not a statistical confidence interval.
	percentile := func(percent int) float64 {
		rank := (len(samples)*percent + 99) / 100
		return float64(samples[rank-1]) / float64(time.Millisecond)
	}
	providerSettings := map[string]any{}
	if selected == "sqlite" {
		for _, name := range []string{"journal_mode", "synchronous", "cache_size", "page_size", "page_count"} {
			var value any
			if err := database.UnsafeSQLX().QueryRowxContext(ctx, "PRAGMA "+name).Scan(&value); err != nil {
				t.Fatal(err)
			}
			providerSettings[name] = value
		}
	} else {
		for _, name := range []string{"server_version", "shared_buffers", "work_mem", "max_connections"} {
			var value string
			if err := database.UnsafeSQLX().GetContext(ctx, &value, "SELECT current_setting($1)", name); err != nil {
				t.Fatal(err)
			}
			providerSettings[name] = value
		}
	}
	summary := map[string]any{
		"profile": profile, "filesystem": filesystem,
		"go": goruntime.Version(), "goos": goruntime.GOOS, "goarch": goruntime.GOARCH, "cpu_available": goruntime.NumCPU(), "gomaxprocs": goruntime.GOMAXPROCS(0), "GOMEMLIMIT": os.Getenv("GOMEMLIMIT"), "GOGC": os.Getenv("GOGC"), "pool_max_open": database.UnsafeSQLX().Stats().MaxOpenConnections, "provider_settings": providerSettings,
		"videos": videos, "media": videos, "copies": 15000, "authorized": len(expected), "batch_graphs": graphsPerBatch, "actual_max_causation_facts": maxGroup, "actual_max_causation_evidence_bytes": maxBytes, "writer": 4, "pending_facts": facts, "seed_ms": float64(seedElapsed) / float64(time.Millisecond),
		"relation_indexes": os.Getenv("GOLEM_CHARACTERIZE_FRAMES_25K_INDEXES"), "relation_fanout_limit": 1, "authorized_count_ms": float64(countElapsed) / float64(time.Millisecond),
		"query":        "member named FileGranted AND parent Media.Visible; ID/MediaID/StorageID; ORDER BY ID ASC; TAKE100; no offset",
		"cache":        "same caller/context; no OS/provider cache flush; initial Explain before seed; first query after seed; ten warmups after full-set correctness and populated Explain; provider defaults/background maintenance retained; no explicit ANALYZE; no publisher",
		"policy_cache": "default runtime actor cache; no custom cache configuration", "samples": len(samples), "first_query_ms": float64(first) / float64(time.Millisecond), "p50_ms": percentile(50), "p95_ms": percentile(95), "p99_ms": percentile(99), "max_ms": float64(samples[len(samples)-1]) / float64(time.Millisecond),
		"plan_status": planStatus,
		"heap_alloc_before": beforeMemory.HeapAlloc, "heap_alloc_after": afterMemory.HeapAlloc, "heap_sys_after": afterMemory.HeapSys,
		"claim": "opt-in characterization only; no performance threshold, production baseline, NAS or cold-cache claim",
	}
	if planStatus == "available" {
		summary["plan_nodes"], summary["plan_warnings"] = plan, report.Warnings()
		summary["plan_min_statements"], summary["plan_max_statements"] = report.MinimumExecutionStatements(), report.MaximumExecutionStatements()
		summary["plan_digest"] = fmt.Sprintf("%x", report.CanonicalDigest())
	}
	encoded, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("FRAMES_25K_CHARACTERIZATION %s", encoded)
}
`

func framesCharacterizationIndexes(t *testing.T) func(string) string {
	t.Helper()
	return func(source string) string {
		for _, entry := range []struct{ model, declaration string }{
			{"Video", "index=characterization_videos_media(media_id)"},
			{"StorageObject", "index=characterization_storage_video(video_id)"},
		} {
			anchor := "type " + entry.model + " struct {"
			if strings.Count(source, anchor) != 1 {
				t.Fatalf("characterization index anchor changed: %s", entry.model)
			}
			source = strings.Replace(source, anchor, anchor+"\n _ struct{} `golem:\""+entry.declaration+"\"`", 1)
		}
		return source
	}
}

// Compose the same declared FK indexes with one real semantic Title index.
// This rewrite is exclusive to the mixed fixture; the 25k schema is unchanged.
func framesMixedSemanticSchema(t *testing.T) func(string) string {
	t.Helper()
	indexes := framesCharacterizationIndexes(t)
	return func(source string) string {
		provider := "golem.Providers(schema, golem.SQLite, golem.PostgreSQL)"
		model := "func (Media) GolemModel() golem.ModelSpec[Media] {\n\treturn golem.DefineModel("
		if strings.Count(source, provider) != 1 || strings.Count(source, model) != 1 {
			t.Fatal("mixed semantic declaration anchors changed")
		}
		source = strings.Replace(source, provider, "golem.Providers(schema, golem.SQLite)\n\tgolem.EmbeddingSpace(schema, \"maintenance\", 3)", 1)
		source = strings.Replace(source, model, model+"golem.SemanticIndex(\"titles\", \"maintenance\", Medias.Title), ", 1)
		return indexes(source)
	}
}

// P00's local server/semantic contention slice optionally includes real SDK
// recovery. Exact connection hold/renewal slack/commit timestamps, NAS and
// comparison against Frames remain separate.
func TestFreshGeneratedFramesMixedServerContention(t *testing.T) {
	if os.Getenv("GOLEM_CHARACTERIZE_FRAMES_MIXED") != "1" {
		t.Skip("opt in with GOLEM_CHARACTERIZE_FRAMES_MIXED=1")
	}
	if os.Getenv("GOLEM_POLICY_HISTORY_WRITER_REHEARSAL") != "4" {
		t.Fatal("mixed contention requires explicit writer4 rehearsal")
	}
	if os.Getenv("GOLEM_CHARACTERIZE_FRAMES_MIXED_SDK") == "1" {
		clientRoot := os.Getenv("GOLEM_GENERATED_CLIENT_ROOT")
		if !filepath.IsAbs(clientRoot) {
			t.Fatal("SDK mode requires absolute GOLEM_GENERATED_CLIENT_ROOT")
		}
		if _, err := exec.LookPath("node"); err != nil {
			t.Fatal("SDK mode requires installed node", err)
		}
		for _, name := range []string{"vitest/vitest.mjs", "@tanstack/query-core/package.json", "graphql-ws/package.json", "graphql/package.json"} {
			if _, err := os.Stat(filepath.Join(clientRoot, "node_modules", name)); err != nil {
				t.Fatalf("SDK dependency %s unavailable: %v", name, err)
			}
		}
	}
	duration := 12 * time.Second
	if value := os.Getenv("GOLEM_CHARACTERIZE_FRAMES_MIXED_DURATION"); value != "" {
		parsed, err := time.ParseDuration(value)
		if err != nil || parsed < 12*time.Second || parsed > 30*time.Minute {
			t.Fatal("mixed duration must be within 12s..30m")
		}
		duration = parsed
	}
	anchor := "\t\"time\""
	if strings.Count(framesFactsConsumerTest, anchor) != 1 {
		t.Fatal("consumer imports changed")
	}
	consumer := strings.Replace(framesFactsConsumerTest, anchor, anchor+"\n \"sort\"\n \"sync\"\n \"github.com/eleven-am/golem/go/queue\"\n \"github.com/eleven-am/golem/go/embedding\"\n \"os/exec\"\n \"path/filepath\"", 1) + maintenanceEmbeddingProviderSource + framesMixedConsumerTest
	runGeneratedDTOConsumerArtifacts(t, "framesfacts", framesMixedSemanticSchema(t), consumer, generatedDTOExecution{writer4Only: true, sqliteDirectory: os.Getenv("GOLEM_CHARACTERIZE_FRAMES_SQLITE_DIRECTORY"), coordinate: func(t *testing.T, artifacts generatedDTOArtifacts) {
		ctx, cancel := context.WithTimeout(context.Background(), duration+5*time.Minute)
		defer cancel()
		command := exec.CommandContext(ctx, artifacts.Writer4, "-test.v", "-test.count=1", "-test.timeout="+(duration+4*time.Minute).String(), "-test.run=^TestFramesMixedServerContention$")
		command.Dir = artifacts.Directory
		command.Env = append(append([]string(nil), artifacts.Environment...), "GOLEM_OBJECT_PROVIDER=sqlite", "GOLEM_OBJECT_DB="+artifacts.DSN, "GOLEM_CHARACTERIZE_FRAMES_MIXED_DURATION="+duration.String())
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Run(); err != nil {
			t.Fatalf("mixed contention: %v", err)
		}
	}}, compilerir.SQLite)
}

const framesMixedConsumerTest = `
type framesMixedCancellation struct {
	At    time.Time
	Cause string
}

type framesMixedObserver struct{ empty atomic.Int64 }

func (value *framesMixedObserver) ObserveGolem(_ context.Context, event observe.Observation) {
	if event.Operation() == observe.OperationEventPublisherClaim && event.Outcome() == observe.OutcomeSuccess && event.AggregateCount() == 0 {
		value.empty.Add(1)
	}
}
func framesMixedQuantiles(t *testing.T, label string, samples []time.Duration) {
	t.Helper()
	if len(samples) == 0 {
		t.Fatalf("no %s samples", label)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	at := func(percent int) time.Duration { return samples[(len(samples)*percent+99)/100-1] }
	t.Logf("%s n=%d p50=%s p95=%s p99=%s max=%s", label, len(samples), at(50), at(95), at(99), samples[len(samples)-1])
}
func TestFramesMixedServerContention(t *testing.T) {
	if os.Getenv("GOLEM_CHARACTERIZE_FRAMES_MIXED") != "1" || os.Getenv("GOLEM_POLICY_HISTORY_WRITER_REHEARSAL") != "4" || os.Getenv("GOLEM_OBJECT_PROVIDER") != "sqlite" {
		t.Fatal("explicit SQLite writer4 opt-in required")
	}
	duration, err := time.ParseDuration(os.Getenv("GOLEM_CHARACTERIZE_FRAMES_MIXED_DURATION"))
	if err != nil || duration < 12*time.Second || duration > 30*time.Minute {
		t.Fatal("invalid bounded duration")
	}
	sdkEnabled := os.Getenv("GOLEM_CHARACTERIZE_FRAMES_MIXED_SDK") == "1"
	stableMedia, stableVideo := framesID(950001), framesID(950002)
	baseline := int64(0)
	if sdkEnabled {
		baseline = 1
	}
	var sdkRevision atomic.Uint32
	var sdkFinished atomic.Bool
	ctx, cancel := context.WithTimeout(context.Background(), duration+3*time.Minute)
	defer cancel()
	database, err := providersqlite.OpenNetworkFilesystem(ctx, providersqlite.Config{DataSourceName: os.Getenv("GOLEM_OBJECT_DB")})
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	transport, err := events.NewMemoryTransport(events.MemoryLimits{})
	if err != nil {
		t.Fatal(err)
	}
	spec, err := embedding.NewSpecification("test", "maintenance-constant", "v1", 3, 8)
	if err != nil {
		t.Fatal(err)
	}
	embedder := &maintenanceEmbedder{spec: spec}
	embeddings, err := embedding.NewRegistry(map[string]embedding.Provider{"maintenance": embedder})
	if err != nil {
		t.Fatal(err)
	}
	registry := queue.NewRegistry()
	release := make(chan struct{})
	releaseWork := sync.OnceFunc(func() { close(release) })
	admitted := make(chan int, 3)
	var calls [3]atomic.Int64
	var cancellations [3]atomic.Pointer[framesMixedCancellation]
	diagnosticOrigin := time.Now()
	var scannerStarted, readStarted, scannerLastDuration, readLastDuration atomic.Int64
	inFlightAge := func(start *atomic.Int64) time.Duration {
		stamp := start.Load()
		if stamp == 0 {
			return 0
		}
		return time.Since(diagnosticOrigin) - time.Duration(stamp-1)
	}
	recordCancellation := func(jobCtx context.Context, index int) {
		cause := fmt.Sprint(context.Cause(jobCtx))
		if len(cause) > 256 {
			cause = cause[:256]
		}
		cancellations[index].CompareAndSwap(nil, &framesMixedCancellation{At: time.Now(), Cause: cause})
	}
	jobType, err := queue.Register(registry, queue.Definition[int]{Type: "frames.mixed-work", MaxAttempts: 2, Timeout: duration + 3*time.Minute, MaxConcurrent: 3, Handle: func(jobCtx context.Context, job queue.Job[int]) error {
		if job.Payload < 0 || job.Payload >= 3 || job.Attempt != 1 {
			return fmt.Errorf("unexpected job admission")
		}
		calls[job.Payload].Add(1)
		select {
		case admitted <- job.Payload:
		case <-jobCtx.Done():
			recordCancellation(jobCtx, job.Payload)
			return jobCtx.Err()
		}
		// Processing deliberately owns no database transaction or connection.
		select {
		case <-release:
			return nil
		case <-jobCtx.Done():
			recordCancellation(jobCtx, job.Payload)
			return jobCtx.Err()
		}
	}})
	if err != nil {
		t.Fatal(err)
	}
	storageA, storageB := framesID(900001), framesID(900002)
	observer := &framesMixedObserver{}
	application, err := app.Open(ctx, app.Config[app.Principal]{Database: database, EventTransport: transport, Observer: observer, Embeddings: embeddings,
		Queue:               &golemruntime.QueueConfig{Registry: registry, Limits: queue.Limits{Concurrency: 4, ClaimBatch: 4, LeaseDuration: 3 * time.Second, PollInterval: 250 * time.Millisecond, RetentionEvery: queue.RetentionDisabled}},
		EventLimits:         events.Limits{RetentionEvery: events.RetentionDisabled},
		ReadLimits:          golemruntime.ReadLimits{MaxRelationDepth: 6, MaxRelationFanout: 1},
		ReportEventOperator: func(context.Context, events.OperatorAuditRecord) { t.Error("unexpected durable event operation") },
		ResolvePrincipal: func(_ context.Context, p app.Principal) (app.Actor, error) {
			if p != "member" {
				return app.Actor{}, fmt.Errorf("unknown principal")
			}
			return app.Actor{DomainStorageIDs: []golem.UUID{storageA}, GrantedStorageIDs: []golem.UUID{storageA}}, nil
		}, SnapshotActor: framesActorSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	caller, err := application.ForPrincipal(ctx, "member")
	if err != nil {
		t.Fatal(err)
	}
	stream, err := caller.Medias.Events(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	bgCtx, stop := context.WithCancel(ctx)
	workerDone, publisherDone := make(chan error, 1), make(chan error, 1)
	go func() { workerDone <- application.RunQueueWorker(bgCtx) }()
	go func() { publisherDone <- application.RunEventPublisher(bgCtx) }()
	defer func() {
		releaseWork()
		stop()
		for _, done := range []chan error{workerDone, publisherDone} {
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("background shutdown: %v", err)
				}
			case <-time.After(10 * time.Second):
				t.Error("background failed to stop")
			}
		}
	}()
	var jobs [3]queue.JobID
	for index := range jobs {
		pending, err := jobType.New(index)
		if err != nil {
			t.Fatal(err)
		}
		jobs[index], err = application.Enqueue(ctx, pending)
		if err != nil {
			t.Fatal(err)
		}
	}
	seenAdmission := map[int]bool{}
	for len(seenAdmission) < 3 {
		select {
		case index := <-admitted:
			if seenAdmission[index] {
				t.Fatal("duplicate admission")
			}
			seenAdmission[index] = true
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	// Only these three owned job rows are read. No payload/token access and no
	// queue writes. Public Inspect supplies the final outcomes; it omits expiry.
	var previous [3]int64
	var renewals [3]int
	var leaseSamples [3]int
	var minimumRemaining [3]int64
	var lastLeaseSample [3]time.Time
	var minimumInterval, maximumInterval [3]time.Duration
	sampleLeases := func() {
		t.Helper()
		for index, id := range jobs {
			var row struct {
				Expiry    int64  ` + "`" + `db:"lease_until"` + "`" + `
				Remaining int64  ` + "`" + `db:"remaining"` + "`" + `
				Attempt   int    ` + "`" + `db:"attempt_count"` + "`" + `
				State     string ` + "`" + `db:"status"` + "`" + `
			}
			queryStarted := time.Now()
			waitBefore := database.UnsafeSQLX().Stats()
			queryErr := database.UnsafeSQLX().GetContext(ctx, &row, "SELECT lease_until,attempt_count,status,lease_until-CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER) AS remaining FROM golem_queue WHERE id=?", id)
			queryFinished := time.Now()
			waitAfter := database.UnsafeSQLX().Stats()
			if queryErr != nil || row.State != "leased" || row.Attempt != 1 || row.Remaining <= 0 {
				interval := time.Duration(0)
				if !lastLeaseSample[index].IsZero() {
					interval = queryFinished.Sub(lastLeaseSample[index])
				}
				// Snapshot causes before any failure defer releases/cancels the workers.
				// Nil means the handler has not observed cancellation at this instant.
				for job := range jobs {
					t.Logf("pre-cleanup handler%d cancellation=%+v", job, cancellations[job].Load())
				}
				t.Fatalf("job%d invalid active lease: state=%s attempt=%d expiry_us=%d DB_remaining_us=%d previous_expiry_us=%d last_sample_interval=%s query_elapsed=%s pool_wait_count_delta=%d pool_wait_duration_delta=%s pool_in_use=%d pool_idle=%d scanner_last_completed=%s read_last_completed=%s scanner_inflight_age=%s read_inflight_age=%s samples=%d minimum_observed_remaining_us=%d renewal_advances=%d query_error=%v", index, row.State, row.Attempt, row.Expiry, row.Remaining, previous[index], interval, queryFinished.Sub(queryStarted), waitAfter.WaitCount-waitBefore.WaitCount, waitAfter.WaitDuration-waitBefore.WaitDuration, waitAfter.InUse, waitAfter.Idle, time.Duration(scannerLastDuration.Load()), time.Duration(readLastDuration.Load()), inFlightAge(&scannerStarted), inFlightAge(&readStarted), leaseSamples[index], minimumRemaining[index], renewals[index], queryErr)
			}
			// Same SQLite Unix-microsecond clock used by the native renewal owner.
			// This is remaining lease at a sample, never slack at renewal execution.
			sampledAt := time.Now()
			if leaseSamples[index] == 0 || row.Remaining < minimumRemaining[index] {
				minimumRemaining[index] = row.Remaining
			}
			if !lastLeaseSample[index].IsZero() {
				interval := sampledAt.Sub(lastLeaseSample[index])
				if leaseSamples[index] == 1 || interval < minimumInterval[index] {
					minimumInterval[index] = interval
				}
				if interval > maximumInterval[index] {
					maximumInterval[index] = interval
				}
			}
			lastLeaseSample[index] = sampledAt
			leaseSamples[index]++
			if previous[index] != 0 && row.Expiry > previous[index] {
				renewals[index]++
			}
			if row.Expiry < previous[index] {
				t.Fatal("lease expiry regressed")
			}
			previous[index] = row.Expiry
		}
	}
	sampleLeases()
	var eventMu sync.Mutex
	type deliveryKey struct {
		ID     golem.UUID
		Action golem.EventAction
	}
	expected := map[deliveryKey]int{}
	delivered := map[deliveryKey]int{}
	seenEvents := map[golem.EventID]bool{}
	var lags []time.Duration
	recvCtx, stopRecv := context.WithCancel(ctx)
	recvExited := make(chan struct{})
	recvErrors := make(chan error, 1)
	var nativeResync atomic.Bool
	go func() {
		defer close(recvExited)
		for {
			event, err := stream.Recv(recvCtx)
			if err != nil {
				if recvCtx.Err() == nil {
					code, codeOK := events.CodeOf(err)
					reason, reasonOK := events.ReasonOf(err)
					if codeOK && reasonOK && code == events.CodeSubscriptionResync && reason == events.ReasonDependencyChanged {
						// This terminal explicitly ends native coverage. SDK mode owns recovery;
						// do not manufacture a native replay/reopen loop or hide other errors.
						nativeResync.Store(true)
					} else {
						recvErrors <- err
					}
				}
				return
			}
			eventMu.Lock()
			id := event.ID()
			key := deliveryKey{id, event.Metadata().Action()}
			factID := event.Metadata().EventID()
			valid := expected[key] > delivered[key] && !seenEvents[factID]
			if valid {
				delivered[key]++
				seenEvents[factID] = true
				lags = append(lags, time.Since(event.Metadata().RecordedAt()))
			}
			eventMu.Unlock()
			if !valid {
				recvErrors <- fmt.Errorf("foreign/duplicate media event %s", id)
				return
			}
		}
	}()
	checkSubscription := func() {
		t.Helper()
		select {
		case err := <-recvErrors:
			t.Fatal(err)
		default:
		}
	}
	defer func() {
		stopRecv()
		stream.Close()
		select {
		case <-recvExited:
		case <-time.After(5 * time.Second):
			t.Error("event reader failed to stop")
		}
		select {
		case err := <-recvErrors:
			t.Error(err)
		default:
		}
	}()

	var sdkExit <-chan struct{}
	sdkErrors := make(chan error, 1)
	checkSDK := func() {
		t.Helper()
		select {
		case err := <-sdkErrors:
			t.Fatal(err)
		default:
		}
	}
	if sdkEnabled {
		eventMu.Lock()
		expected[deliveryKey{stableMedia, golem.EventCreated}]++
		eventMu.Unlock()
		if err := application.System().Transaction(ctx, func(tx *app.SystemTx[app.Principal]) error {
			if _, err := tx.Medias.Create(ctx, app.Medias.Create(app.Medias.ID.Create(stableMedia), app.Medias.Title.Create("mixed-title-0"), app.Medias.AgeRating.Create(12))); err != nil {
				return err
			}
			_, err := tx.Videos.Create(ctx, app.Videos.Create(app.Videos.ID.Create(stableVideo), app.Videos.MediaID.Create(stableMedia), app.Videos.StorageID.Create(storageA)))
			return err
		}); err != nil {
			t.Fatal(err)
		}
		graph, err := application.GraphQL(app.GraphQLConfig[app.Principal]{PrincipalFromContext: func(ctx context.Context) (app.Principal, bool) {
			principal, ok := ctx.Value(framesGraphQLPrincipalKey{}).(app.Principal)
			return principal, ok
		}, ReportInternalError: func(_ context.Context, err error) { t.Errorf("trusted GraphQL failure: %v", err) }})
		if err != nil {
			t.Fatal(err)
		}
		ready := make(chan struct{})
		var readyOnce sync.Once
		deadline, _ := ctx.Deadline()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/test/ready":
				if r.Method != http.MethodPost {
					http.Error(w, "POST required", 405)
					return
				}
				readyOnce.Do(func() { close(ready) })
				w.WriteHeader(204)
			case "/test/state":
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(map[string]any{"done": sdkFinished.Load(), "revision": sdkRevision.Load(), "video": stableVideo.String()})
			default:
				if r.Header.Get("x-golem-profile") != "member" {
					http.Error(w, "principal required", 401)
					return
				}
				requestCtx, stopRequest := context.WithDeadline(r.Context(), deadline)
				defer stopRequest()
				requestCtx = context.WithValue(requestCtx, framesGraphQLPrincipalKey{}, app.Principal("member"))
				graph.Handler().ServeHTTP(w, r.WithContext(requestCtx))
			}
		}))
		defer server.Close()
		clientRoot := os.Getenv("GOLEM_GENERATED_CLIENT_ROOT")
		if !filepath.IsAbs(clientRoot) {
			t.Fatal("absolute generated client root required")
		}
		manifest, err := filepath.Abs("zz_golem_client.gen.ts")
		if err != nil {
			t.Fatal(err)
		}
		nodeCtx, stopNode := context.WithTimeout(ctx, duration+2*time.Minute)
		command := exec.CommandContext(nodeCtx, "node", filepath.Join(clientRoot, "node_modules/vitest/vitest.mjs"), "run", "--config", "vitest.config.ts", "--testTimeout", fmt.Sprint((duration + 90*time.Second).Milliseconds()))
		command.Dir = filepath.Join(clientRoot, "core")
		command.Env = append(os.Environ(), "GOLEM_GENERATED_HTTP_URL="+server.URL, "GOLEM_GENERATED_HTTP_MANIFEST="+manifest, "GOLEM_GENERATED_CLIENT_FIXTURE=mixed", "GOLEM_CHARACTERIZE_FRAMES_MIXED_DURATION="+duration.String(), "NODE_OPTIONS=--max-old-space-size=256", "UV_THREADPOOL_SIZE=1")
		// Inherit bounded direct output, avoiding a duration-sized in-memory log.
		command.Stdout, command.Stderr = os.Stdout, os.Stderr
		if err := command.Start(); err != nil {
			stopNode()
			t.Fatal(err)
		}
		exited := make(chan struct{})
		sdkExit = exited
		go func() {
			defer close(exited)
			if err := command.Wait(); err != nil && nodeCtx.Err() == nil {
				sdkErrors <- fmt.Errorf("real mixed SDK: %w", err)
			} else if nodeCtx.Err() != nil {
				sdkErrors <- nodeCtx.Err()
			}
		}()
		defer func() {
			stopNode()
			select {
			case <-exited:
			case <-time.After(10 * time.Second):
				t.Error("SDK process did not stop")
			}
		}()
		readiness := time.NewTimer(60 * time.Second)
		defer readiness.Stop()
		select {
		case <-ready:
		case err := <-recvErrors:
			t.Fatal(err)
		case err := <-sdkErrors:
			t.Fatal(err)
		case <-exited:
			t.Fatal("SDK exited before readiness")
		case <-readiness.C:
			t.Fatal("SDK readiness timeout")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		// All initial seed notices are published before normal workload admission;
		// the SDK itself has already observed LIVE and verified the seed title.
	}
	var writes, reads []time.Duration
	var committed atomic.Uint32
	var readsCompleted atomic.Uint64
	var finalCommitObserved atomic.Int64
	semanticOverlap := false
	semanticBeforeOffer := embedder.inputs.Load()
	// Pool-wide counters include workers, publisher and fixture diagnostic SQL.
	// These are aggregate waits, not request-level wait/hold distributions.
	poolBefore := database.UnsafeSQLX().Stats()
	started := time.Now()
	offerEnd := started.Add(duration)
	workCtx, cancelWork := context.WithCancel(ctx)
	var loops sync.WaitGroup
	results := make(chan error, 2)
	// Stop offering at the common deadline, but allow already-started operations
	// to finish. Cancellation is reserved for failure/overall bounded timeout.
	defer func() {
		cancelWork()
		loops.Wait()
		if !t.Failed() {
			return
		}
		// Cancellation may have completed an in-flight operation. Include it, but
		// never present this failed/cleanup-affected distribution as a passing run.
		for _, sample := range []struct {
			name   string
			values []time.Duration
		}{{"scanner", writes}, {"playback", reads}} {
			if len(sample.values) == 0 {
				t.Logf("FAILURE-DIAGNOSTIC %s requests unavailable: zero completed samples", sample.name)
				continue
			}
			values := append([]time.Duration(nil), sample.values...)
			sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
			at := func(percent int) time.Duration { return values[(len(values)*percent+99)/100-1] }
			t.Logf("FAILURE-DIAGNOSTIC %s completed requests n=%d p50=%s p95=%s p99=%s max=%s; cleanup-canceled inflight may be included; NOT successful acceptance", sample.name, len(values), at(50), at(95), at(99), values[len(values)-1])
		}
	}()
	waitOffer := func() error {
		select {
		case <-workCtx.Done():
			return workCtx.Err()
		case <-time.After(250 * time.Millisecond):
			return nil
		}
	}
	readPage := func(readCtx context.Context, lower uint32, exact bool) error {
		rows, err := caller.Videos.FindMany(readCtx, golem.Select[app.Video](app.Videos.ID, app.Videos.MediaID, app.Videos.StorageID), golem.OrderBy(app.Videos.ID.Asc()), golem.Take[app.Video](100), golem.Where(golem.Not(app.Videos.ID.Eq(stableVideo))))
		if err != nil {
			return err
		}
		minimum := int(lower)
		if minimum > 100 {
			minimum = 100
		}
		if len(rows) < minimum || len(rows) > 100 || (exact && len(rows) != minimum) {
			return fmt.Errorf("playback count=%d committed-before-read=%d exact=%t", len(rows), lower, exact)
		}
		// Concurrent commits may precede publication of the atomic counter. Every
		// returned row must still be the exact ordered authorized prefix.
		for index, row := range rows {
			id, ok := golem.Value(row, app.Videos.ID).Get()
			media, mok := golem.Value(row, app.Videos.MediaID).Get()
			storage, sok := golem.Value(row, app.Videos.StorageID).Get()
			if !ok || !mok || !sok || id != framesID(300000+uint32(index)*2) || media != framesID(100000+uint32(index)*2) || storage != storageA {
				return fmt.Errorf("playback identity/FK/authorization mismatch at %d", index)
			}
		}
		return nil
	}
	loops.Add(2)
	go func() {
		defer loops.Done()
		results <- func() error {
			for time.Now().Before(offerEnd) {
				batch := committed.Load()
				if batch >= 10000 {
					return fmt.Errorf("bounded scanner sample capacity exhausted")
				}
				visible, foreign := framesID(100000+batch*2), framesID(100001+batch*2)
				eventMu.Lock()
				expected[deliveryKey{visible, golem.EventCreated}]++
				if sdkEnabled {
					expected[deliveryKey{stableMedia, golem.EventUpdated}]++
				}
				eventMu.Unlock()
				begin := time.Now()
				scannerStarted.Store(time.Since(diagnosticOrigin).Nanoseconds() + 1)
				err := application.System().Transaction(workCtx, func(tx *app.SystemTx[app.Principal]) error {
					for index, id := range []golem.UUID{visible, foreign} {
						storage := storageA
						if index == 1 {
							storage = storageB
						}
						if _, err := tx.Medias.Create(workCtx, app.Medias.Create(app.Medias.ID.Create(id), app.Medias.Title.Create(fmt.Sprintf("scan-%d-%d", batch, index)), app.Medias.AgeRating.Create(12))); err != nil {
							return err
						}
						if _, err := tx.Videos.Create(workCtx, app.Videos.Create(app.Videos.ID.Create(framesID(300000+batch*2+uint32(index))), app.Videos.MediaID.Create(id), app.Videos.StorageID.Create(storage))); err != nil {
							return err
						}
					}
					if sdkEnabled {
						if _, err := tx.Medias.Update(workCtx, app.Medias.ByID.Value(stableMedia), app.Medias.Update(app.Medias.Title.Set(fmt.Sprintf("mixed-title-%d", batch+1)))); err != nil {
							return err
						}
					}
					return nil
				})
				elapsed := time.Since(begin)
				scannerLastDuration.Store(elapsed.Nanoseconds())
				scannerStarted.Store(0)
				writes = append(writes, elapsed)
				if err != nil {
					return fmt.Errorf("scanner: %w", err)
				}
				finalCommitObserved.Store(time.Now().UnixNano())
				committed.Store(batch + 1)
				if sdkEnabled {
					sdkRevision.Store(batch + 1)
				}
				if err := waitOffer(); err != nil {
					return err
				}
			}
			return nil
		}()
	}()
	go func() {
		defer loops.Done()
		results <- func() error {
			for time.Now().Before(offerEnd) {
				if len(reads) >= 10000 {
					return fmt.Errorf("bounded playback sample capacity exhausted")
				}
				lower := committed.Load()
				begin := time.Now()
				readStarted.Store(time.Since(diagnosticOrigin).Nanoseconds() + 1)
				err := readPage(workCtx, lower, false)
				elapsed := time.Since(begin)
				readLastDuration.Store(elapsed.Nanoseconds())
				readStarted.Store(0)
				reads = append(reads, elapsed)
				if err != nil {
					return fmt.Errorf("playback: %w", err)
				}
				readsCompleted.Add(1)
				if err := waitOffer(); err != nil {
					return err
				}
			}
			return nil
		}()
	}()
	leaseTicker := time.NewTicker(250 * time.Millisecond)
	defer leaseTicker.Stop()
	nextProgress := started.Add(time.Minute)
	for finished := 0; finished < 2; {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
			finished++
		case err := <-recvErrors:
			t.Fatal(err)
		case err := <-sdkErrors:
			t.Fatal(err)
		case <-leaseTicker.C:
			sampleLeases()
			if now := time.Now(); !now.Before(nextProgress) {
				t.Logf("mixed progress elapsed=%s committed_batches=%d playback_reads=%d embedding_inputs=%d job_expiry_advances=%v native_terminal=%t sdk=%t", now.Sub(started), committed.Load(), readsCompleted.Load(), embedder.inputs.Load(), renewals, nativeResync.Load(), sdkEnabled)
				nextProgress = now.Add(time.Minute)
			}
			// New embedding inputs were processed inside the offer window.
			// All three handlers still await release; reads and scans made progress.
			if time.Now().Before(offerEnd) && embedder.inputs.Load() > semanticBeforeOffer && committed.Load() > 0 && readsCompleted.Load() > 0 {
				semanticOverlap = true
			}
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	loops.Wait() // no sample slices or final counters are read before both joins
	checkSubscription()
	checkSDK()
	batches := committed.Load()
	sdkFinished.Store(true)
	if batches == 0 || len(reads) == 0 {
		t.Fatal("mixed workload made no progress")
	}
	if err := readPage(ctx, batches, true); err != nil {
		t.Fatal(err)
	}
	if count, err := caller.Videos.Count(ctx); err != nil || count != int64(batches)+baseline {
		t.Fatalf("final authorized count=%d want%d err=%v", count, int64(batches)+baseline, err)
	}
	if count, err := application.System().Videos.Count(ctx); err != nil || count != int64(batches)*2+baseline {
		t.Fatalf("final physical count=%d want%d err=%v", count, int64(batches)*2+baseline, err)
	}
	for index, n := range renewals {
		if n < 2 {
			t.Fatalf("job%d observed only%d persisted expiry advances", index, n)
		}
	}
	if !semanticOverlap {
		t.Fatal("no semantic activity observed during concurrent scanner/playback and three leased jobs")
	}
	// Keep the three jobs leased while the fourth slot completes semantic work.
	// The built-in job type is the same durable worker owner used by maintenance.
	semanticDeadline := time.Now().Add(30 * time.Second)
	for {
		checkSubscription()
		checkSDK()
		sampleLeases()
		counts, err := application.QueueOperator().CountByState(ctx, queue.CountQuery{Types: []string{"semantic.drain"}})
		if err != nil {
			t.Fatal(err)
		}
		if counts.Failed != 0 || counts.Canceled != 0 {
			t.Fatalf("semantic drain failed: %+v", counts)
		}
		if counts.Pending == 0 && counts.Leased == 0 {
			break
		}
		if time.Now().After(semanticDeadline) {
			t.Fatal("semantic drain did not converge")
		}
		select {
		case err := <-recvErrors:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Commit completion is observed at successful mutation return, not an
	// invented internal commit timestamp. Polling adds up to one sample interval.
	semanticDrainElapsed := time.Since(time.Unix(0, finalCommitObserved.Load()))
	semanticInputs := embedder.inputs.Load()
	if semanticInputs == 0 {
		t.Fatal("no actual embedding inputs")
	}
	if sdkEnabled {
		select {
		case <-sdkExit:
			checkSDK()
		case err := <-sdkErrors:
			t.Fatal(err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	releaseWork()
	deadline := time.Now().Add(20 * time.Second)
	for {
		checkSubscription()
		checkSDK()
		complete := true
		for _, id := range jobs {
			status, err := application.QueueOperator().Inspect(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			if status.Attempt != 1 || status.State == queue.StateFailed || status.State == queue.StateCanceled {
				t.Fatalf("unexpected terminal progress: %+v", status)
			}
			if status.State != queue.StateSucceeded {
				complete = false
			} else if status.FinishedAt == nil || status.LastCode != "" {
				t.Fatalf("invalid success: %+v", status)
			}
		}
		if complete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("jobs did not finish")
		}
		time.Sleep(50 * time.Millisecond)
	}
	for index := range calls {
		if calls[index].Load() != 1 {
			t.Fatal("handler reexecuted")
		}
	}
	deadline = time.Now().Add(30 * time.Second)
	for {
		checkSubscription()
		checkSDK()
		eventMu.Lock()
		complete := true
		for key, count := range expected {
			if delivered[key] != count {
				complete = false
			}
		}
		eventMu.Unlock()
		if complete || nativeResync.Load() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("publisher delivery shortfall")
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(50 * time.Millisecond):
		}
	}
	// Await all durable publication acknowledgements, including the foreign
	// graphs, independently of whether the native stream already ended with
	// a declared coverage gap. Durable acknowledgement is not subscriber delivery.
	deadline = time.Now().Add(30 * time.Second)
	for {
		checkSubscription()
		checkSDK()
		var pending int64
		if err := database.UnsafeSQLX().GetContext(ctx, &pending, "SELECT count(*) FROM _golem_outbox_delivery WHERE status<>'delivered'"); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("outbox did not acknowledge all graphs")
		}
		time.Sleep(50 * time.Millisecond)
	}
	select {
	case err := <-recvErrors:
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-time.After(500 * time.Millisecond):
	}
	// One latency sample per causation, never one per fact. All rows are owned
	// by this disposable fixture, whose cap is 10000 batches plus one SDK seed.
	// The source timestamp predates commit; label this recorded-to-ack only.
	ackRows, err := database.UnsafeSQLX().QueryxContext(ctx, "SELECT g.first_recorded,COUNT(d.causation_id),COUNT(d.first_recorded_at),COUNT(d.delivered_at),COALESCE(MIN(d.first_recorded_at),0),COALESCE(MIN(d.delivered_at),0),SUM(CASE WHEN d.status='delivered' THEN 1 ELSE 0 END) FROM (SELECT causation_id,MIN(recorded_at) AS first_recorded FROM _golem_outbox GROUP BY causation_id) g LEFT JOIN _golem_outbox_delivery d ON d.causation_id=g.causation_id GROUP BY g.causation_id,g.first_recorded ORDER BY g.causation_id LIMIT 10002")
	if err != nil {
		t.Fatal(err)
	}
	ackLags := make([]time.Duration, 0, int(batches)+int(baseline))
	readAcknowledgements := func() error {
		defer ackRows.Close()
		for ackRows.Next() {
			if len(ackLags) >= 10001 {
				return fmt.Errorf("causation measurement exceeds fixture cap")
			}
			var recorded, matches, recordedPresent, ackPresent, first, ack, delivered int64
			if err := ackRows.Scan(&recorded, &matches, &recordedPresent, &ackPresent, &first, &ack, &delivered); err != nil {
				return err
			}
			if matches != 1 || recordedPresent != 1 || ackPresent != 1 || delivered != 1 || first != recorded {
				return fmt.Errorf("causation lacks exactly one consistent delivered timestamp row: matches=%d recorded=%d ack=%d delivered=%d", matches, recordedPresent, ackPresent, delivered)
			}
			delta := ack - first
			if delta < 0 {
				return fmt.Errorf("negative first-recorded-to-ack delta: %d microseconds (not clamped)", delta)
			}
			// Maximum time.Duration is a representation bound, not a latency target.
			if delta > int64((time.Duration(1<<63-1))/time.Microsecond) {
				return fmt.Errorf("ack latency duration overflow")
			}
			ackLags = append(ackLags, time.Duration(delta)*time.Microsecond)
		}
		if err := ackRows.Err(); err != nil {
			return err
		}
		return ackRows.Close()
	}
	if err := readAcknowledgements(); err != nil {
		t.Fatal(err)
	}
	if len(ackLags) != int(batches)+int(baseline) {
		t.Fatalf("causation samples=%d want%d", len(ackLags), int(batches)+int(baseline))
	}
	var deliveryRows int64
	if err := database.UnsafeSQLX().GetContext(ctx, &deliveryRows, "SELECT count(*) FROM _golem_outbox_delivery"); err != nil {
		t.Fatal(err)
	}
	if deliveryRows != int64(len(ackLags)) {
		t.Fatal("delivery inventory differs from complete causation inventory")
	}
	poolAfter := database.UnsafeSQLX().Stats()
	if poolAfter.WaitCount < poolBefore.WaitCount || poolAfter.WaitDuration < poolBefore.WaitDuration {
		t.Fatal("pool cumulative wait counters regressed")
	}
	framesMixedQuantiles(t, "complete causation first-recorded-to-delivery-ack", ackLags)
	t.Logf("pool-wide offer-start-through-all-drain wait_count_delta=%d wait_duration_delta=%s before(open=%d in_use=%d idle=%d) after(open=%d in_use=%d idle=%d); includes workers/publisher/diagnostic queries; aggregate only, NOT per-request wait/hold distribution", poolAfter.WaitCount-poolBefore.WaitCount, poolAfter.WaitDuration-poolBefore.WaitDuration, poolBefore.OpenConnections, poolBefore.InUse, poolBefore.Idle, poolAfter.OpenConnections, poolAfter.InUse, poolAfter.Idle)
	for index := range jobs {
		t.Logf("job%d sampled remaining minimum=%dus samples=%d actual observation interval min=%s max=%s; target=250ms during offers and50ms during semantic drain; includes query delay/setup gaps; NOT renewal-time slack", index, minimumRemaining[index], leaseSamples[index], minimumInterval[index], maximumInterval[index])
	}
	// Check real V4 mutation path, not a synthetic insert into fact storage.
	var legacy, facts int64
	if err := database.UnsafeSQLX().GetContext(ctx, &legacy, "SELECT count(*) FROM _golem_outbox WHERE fact_version<>4"); err != nil {
		t.Fatal(err)
	}
	if err := database.UnsafeSQLX().GetContext(ctx, &facts, "SELECT count(*) FROM _golem_outbox"); err != nil {
		t.Fatal(err)
	}
	if legacy != 0 || facts != int64(batches)*(4+baseline)+baseline*2 {
		t.Fatalf("writer facts=%d legacy=%d batches=%d", facts, legacy, batches)
	}
	// The exact-rank API caps take at 1000. Verify the entire corpus in
	// bounded partitions (256 graphs / 512 model identities), without widening
	// that budget or importing vectors. Every vector came from the real worker.
	vector, err := embedding.NewVector([]float32{1, 0, 0})
	if err != nil {
		t.Fatal(err)
	}
	query, err := embedding.NewQueryVector(spec, vector)
	if err != nil {
		t.Fatal(err)
	}
	beforeVectorChecks := embedder.inputs.Load()
	for base := uint32(0); base < batches; base += 256 {
		checkSubscription()
		checkSDK()
		end := base + 256
		if end > batches {
			end = batches
		}
		ids := make([]golem.UUID, 0, 2*(end-base))
		selectors := make([]golem.UniqueSelectorValue[app.Media], 0, 2*(end-base))
		visible := map[golem.UUID]bool{}
		all := map[golem.UUID]bool{}
		for index := base; index < end; index++ {
			for offset := uint32(0); offset < 2; offset++ {
				id := framesID(100000 + index*2 + offset)
				ids = append(ids, id)
				selectors = append(selectors, app.Medias.ByID.Value(id))
				all[id] = true
				if offset == 0 {
					visible[id] = true
				}
			}
		}
		stored, err := application.System().Medias.VectorsTitles(ctx, selectors...)
		if err != nil || len(stored) != len(ids) {
			t.Fatalf("stored vectors count=%d want%d err=%v", len(stored), len(ids), err)
		}
		for index, cell := range stored {
			value, present := cell.Get()
			if !present {
				t.Fatalf("missing stored vector for %s", ids[index])
			}
			values := value.Vector().Values()
			if len(values) != 3 || values[0] != 1 || values[1] != 0 || values[2] != 0 || value.Specification() != spec {
				t.Fatal("stored vector/spec differs from actual fixture embedder")
			}
		}
		publicVectors, err := caller.Medias.VectorsTitles(ctx, selectors...)
		if err != nil || len(publicVectors) != len(ids) {
			t.Fatalf("caller vectors: %v", err)
		}
		for index, cell := range publicVectors {
			_, present := cell.Get()
			if present != (index%2 == 0) {
				t.Fatalf("vector disclosure mismatch for %s", ids[index])
			}
		}
		systemResults, err := application.System().Medias.SearchByVectorTitles(ctx, query, len(ids), app.Medias.ID.In(ids...))
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range systemResults {
			id, present := golem.Value(result.Row(), app.Medias.ID).Get()
			if !present || !all[id] {
				t.Fatal("unexpected/duplicate system semantic identity")
			}
			delete(all, id)
		}
		if len(all) != 0 {
			t.Fatalf("missing %d system semantic identities", len(all))
		}
		results, err := caller.Medias.SearchByVectorTitles(ctx, query, len(ids), app.Medias.ID.In(ids...))
		if err != nil {
			t.Fatal(err)
		}
		for _, result := range results {
			id, present := golem.Value(result.Row(), app.Medias.ID).Get()
			if !present || !visible[id] {
				t.Fatal("foreign/duplicate semantic result")
			}
			delete(visible, id)
		}
		if len(visible) != 0 {
			t.Fatalf("missing %d authorized semantic identities", len(visible))
		}
	}
	if sdkEnabled {
		stable, err := caller.Medias.VectorsTitles(ctx, app.Medias.ByID.Value(stableMedia))
		if err != nil || len(stable) != 1 {
			t.Fatalf("stable vector: %v", err)
		}
		value, present := stable[0].Get()
		if !present || value.Specification() != spec {
			t.Fatal("stable vector absent/stale specification")
		}
		results, err := caller.Medias.SearchByVectorTitles(ctx, query, 1, app.Medias.ID.Eq(stableMedia))
		if err != nil || len(results) != 1 {
			t.Fatalf("stable semantic result: %v", err)
		}
		id, present := golem.Value(results[0].Row(), app.Medias.ID).Get()
		if !present || id != stableMedia {
			t.Fatal("wrong stable semantic identity")
		}
	}
	if embedder.inputs.Load() != beforeVectorChecks {
		t.Fatal("vector inspection/search unexpectedly invoked embedding provider")
	}
	t.Logf("semantic inputs=%d overlap=true final-commit-return-to-complete-drain=%s drain-poll=50ms deterministic-local-3D-provider; no external embedding latency claim", semanticInputs, semanticDrainElapsed)
	framesMixedQuantiles(t, "scanner transaction", writes)
	framesMixedQuantiles(t, "authorized playback page", reads)
	eventMu.Lock()
	lagSamples := append([]time.Duration(nil), lags...)
	eventMu.Unlock()
	if nativeResync.Load() {
		t.Log("native stream terminal=GOLEM_SUBSCRIPTION_RESYNC reason=DEPENDENCY_CHANGED; coverage ends at terminal; complete delivery/full-window nondisclosure NOT established")
		if len(lagSamples) == 0 {
			t.Log("native fact-recorded-to-subscriber lag unavailable: no deliveries before terminal")
		} else {
			framesMixedQuantiles(t, "native fact-recorded-to-subscriber delivery PARTIAL before terminal", lagSamples)
		}
	} else {
		framesMixedQuantiles(t, "native fact-recorded-to-subscriber delivery (live stream exact delivery check)", lagSamples)
	}
	storageProfile := "SQLite-network-filesystem-on-owned-local-temp NOT-NAS"
	if os.Getenv("GOLEM_CHARACTERIZE_FRAMES_SQLITE_DIRECTORY") != "" {
		storageProfile = "SQLite-network-filesystem-policy-on-owned-external-scratch; physical mount requires separate verification"
	}
	t.Logf("profile=%s duration=%s batches=%d jobs=3 lease=3s poll=250ms expiry-advances=%v empty-publisher-claims=%d; lease-sampling=250ms-target-with-query-delay; independent-scanner/playback-offers=250ms-after-completion; no cache flush; per-request connection wait/hold distributions, exact renewal-time slack, exact commit timestamps and Frames baseline NOT measured; SDK recovery measured only when explicit SDK mode enabled", storageProfile, time.Since(started), batches, renewals, observer.empty.Load())
}
`
