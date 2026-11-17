package framesfacts

import (
	"context"
	"time"

	"github.com/eleven-am/golem/go/golem"
)

type Principal string

type Actor struct {
	Owner              bool
	Guest              bool
	Capped             bool
	MaxAge             int64
	ReadStorageObjects bool

	DomainStorageIDs      []golem.UUID
	GrantedStorageIDs     []golem.UUID
	OpenStorageIDs        []golem.UUID
	RestrictedStorageIDs  []golem.UUID
	DeniedStorageIDs      []golem.UUID
	AllowedMediaIDs       []golem.UUID
	DeniedMediaIDs        []golem.UUID
	RemovedMediaIDs       []golem.UUID
	OwnerExcludedMediaIDs []golem.UUID
	OwnedProfileIDs       []golem.UUID
	VisiblePlaylistIDs    []golem.UUID
	OwnedPlaylistIDs      []golem.UUID
}

type LibraryDomainArgs struct {
	StorageIDs []golem.UUID
	MediaIDs   []golem.UUID
}

type OwnerCatalogArgs struct {
	ExcludedMediaIDs []golem.UUID
}

type MediaAccessArgs struct {
	AllowedMediaIDs      []golem.UUID
	GrantedStorageIDs    []golem.UUID
	OpenStorageIDs       []golem.UUID
	RestrictedStorageIDs []golem.UUID
	DeniedStorageIDs     []golem.UUID
	DeniedMediaIDs       []golem.UUID
	RemovedMediaIDs      []golem.UUID
}

type OwnerAccessArgs struct {
	DeniedStorageIDs []golem.UUID
	DeniedMediaIDs   []golem.UUID
	RemovedMediaIDs  []golem.UUID
}

type VideoAccessArgs struct {
	AllowedMediaIDs   []golem.UUID
	GrantedStorageIDs []golem.UUID
	OpenStorageIDs    []golem.UUID
}

type RatingArgs struct {
	MaxAge int64
}

type OwnerRetitleArgs struct {
	ID    golem.UUID
	Title string
}

type Media struct {
	_ struct{} `golem:"model;table=media"`

	ID        golem.UUID `db:"id" golem:"pk;default=uuid"`
	Title     string     `db:"title" golem:"type=varchar(120)"`
	AgeRating *int64     `db:"age_rating"`
	Adult     bool       `db:"adult" golem:"default=false"`
	RetiredAt *time.Time `db:"retired_at"`
	Videos    []Video    `db:"-" golem:"relation=has_many;fields=id;references=media_id"`
	Episodes  []Episode  `db:"-" golem:"relation=has_many;fields=id;references=show_id"`
}

type Video struct {
	_ struct{} `golem:"model;table=videos"`

	ID              golem.UUID      `db:"id" golem:"pk;default=uuid"`
	MediaID         golem.UUID      `db:"media_id"`
	StorageID       golem.UUID      `db:"storage_id"`
	RetiredAt       *time.Time      `db:"retired_at"`
	Media           *Media          `db:"-" golem:"relation=belongs_to;fields=media_id;references=id"`
	StorageObjects  []StorageObject `db:"-" golem:"relation=has_many;fields=id;references=video_id"`
	Episodes        []Episode       `db:"-" golem:"relation=has_many;fields=id;references=video_id"`
	WatchProgresses []WatchProgress `db:"-" golem:"relation=has_many;fields=id;references=video_id"`
	PlaylistVideos  []PlaylistVideo `db:"-" golem:"relation=has_many;fields=id;references=video_id"`
}

type StorageObject struct {
	_ struct{} `golem:"model;table=storage_objects"`

	ID        golem.UUID `db:"id" golem:"pk;default=uuid"`
	VideoID   golem.UUID `db:"video_id"`
	StorageID golem.UUID `db:"storage_id"`
	State     string     `db:"state" golem:"type=varchar(32)"`
	Video     *Video     `db:"-" golem:"relation=belongs_to;fields=video_id;references=id"`
}

type Episode struct {
	_ struct{} `golem:"model;table=episodes"`
	_ struct{} `golem:"unique=uq_episodes_show_number(show_id,season,number)"`

	ID      golem.UUID `db:"id" golem:"pk;default=uuid"`
	ShowID  golem.UUID `db:"show_id"`
	VideoID golem.UUID `db:"video_id"`
	Season  int64      `db:"season"`
	Number  int64      `db:"number"`
	Show    *Media     `db:"-" golem:"relation=belongs_to;fields=show_id;references=id"`
	Video   *Video     `db:"-" golem:"relation=belongs_to;fields=video_id;references=id"`
}

type WatchProgress struct {
	_ struct{} `golem:"model;table=watch_progresses"`
	_ struct{} `golem:"unique=uq_progress_profile_video(profile_id,video_id)"`

	ID         golem.UUID `db:"id" golem:"pk;default=uuid"`
	ProfileID  golem.UUID `db:"profile_id"`
	VideoID    golem.UUID `db:"video_id"`
	Percentage float64    `db:"percentage" golem:"default=0"`
	Video      *Video     `db:"-" golem:"relation=belongs_to;fields=video_id;references=id"`
}

type PlaylistVideo struct {
	_ struct{} `golem:"model;table=playlist_videos"`
	_ struct{} `golem:"unique=uq_playlist_video(playlist_id,video_id)"`

	ID         golem.UUID `db:"id" golem:"pk;default=uuid"`
	PlaylistID golem.UUID `db:"playlist_id"`
	VideoID    golem.UUID `db:"video_id"`
	Index      int64      `db:"position" golem:"default=0"`
	Video      *Video     `db:"-" golem:"relation=belongs_to;fields=video_id;references=id"`
}

func DefineSchema(schema *golem.Schema) {
	golem.SchemaName(schema, "frames_policy_facts")
	golem.Actor[Actor](schema)
	golem.Model[Media](schema)
	golem.Model[Video](schema)
	golem.Model[StorageObject](schema)
	golem.Model[Episode](schema)
	golem.Model[WatchProgress](schema)
	golem.Model[PlaylistVideo](schema)
	golem.Providers(schema, golem.SQLite, golem.PostgreSQL)
}

func DefineOperations(operations *golem.Operations) {
	golem.TxOperation(operations, "ownerRetitle", OwnerRetitle)
}

func OwnerRetitle(ctx context.Context, transaction *CallerTx[Principal], args OwnerRetitleArgs) (int64, error) {
	if !golem.ActorFrom[Actor](ctx).Owner {
		return 0, golem.RuntimeOperationError(golem.CodeForbidden, "ownerRetitle", golem.ModelID{}, golem.FieldID{}, "owner authority is required", nil)
	}
	return SystemEscape(transaction).Medias.UpdateMany(ctx, Medias.ID.Eq(args.ID), Medias.UpdateMany(Medias.Title.Set(args.Title)))
}

func (Media) GolemModel() golem.ModelSpec[Media] {
	return golem.DefineModel(golem.Subscriptions(
		golem.ScopeTemplate("libraries", func(args LibraryDomainArgs) golem.Predicate[Media] {
			return Medias.ID.In(args.MediaIDs...).Or(Medias.Videos.Some(
				Videos.StorageID.In(args.StorageIDs...).Or(Videos.StorageObjects.Some(
					StorageObjects.StorageID.In(args.StorageIDs...).And(StorageObjects.State.In("PRESENT", "MISSING")),
				)),
			))
		}),
		golem.ScopeTemplate("owner", func(args OwnerCatalogArgs) golem.Predicate[Media] {
			return Medias.ID.In(args.ExcludedMediaIDs...).Not()
		}),
		golem.FactTemplate("visible", "libraries", func(args MediaAccessArgs) golem.Predicate[Media] {
			return Medias.RetiredAt.IsNull().And(
				Medias.ID.NotIn(args.DeniedMediaIDs...), Medias.ID.NotIn(args.RemovedMediaIDs...),
				Medias.Videos.None(Videos.StorageID.In(args.DeniedStorageIDs...).Or(Videos.StorageObjects.Some(
					StorageObjects.StorageID.In(args.DeniedStorageIDs...).And(StorageObjects.State.In("PRESENT", "MISSING")),
				))),
				Medias.ID.In(args.AllowedMediaIDs...).Or(
					Medias.Videos.Some(Videos.StorageID.In(args.GrantedStorageIDs...).Or(Videos.StorageObjects.Some(
						StorageObjects.StorageID.In(args.GrantedStorageIDs...).And(StorageObjects.State.In("PRESENT", "MISSING")),
					))),
					Medias.Videos.Some(Videos.StorageID.In(args.OpenStorageIDs...).Or(Videos.StorageObjects.Some(
						StorageObjects.StorageID.In(args.OpenStorageIDs...).And(StorageObjects.State.In("PRESENT", "MISSING")),
					))).And(Medias.Videos.None(Videos.StorageID.In(args.RestrictedStorageIDs...).Or(Videos.StorageObjects.Some(
						StorageObjects.StorageID.In(args.RestrictedStorageIDs...).And(StorageObjects.State.In("PRESENT", "MISSING")),
					)))),
				),
			)
		}),
		golem.FactTemplate("visibleOwner", "owner", func(args OwnerAccessArgs) golem.Predicate[Media] {
			return Medias.RetiredAt.IsNull().And(
				Medias.ID.NotIn(args.DeniedMediaIDs...), Medias.ID.NotIn(args.RemovedMediaIDs...),
				Medias.Videos.None(Videos.StorageID.In(args.DeniedStorageIDs...).Or(Videos.StorageObjects.Some(
					StorageObjects.StorageID.In(args.DeniedStorageIDs...).And(StorageObjects.State.In("PRESENT", "MISSING")),
				))),
			)
		}),
		golem.FactTemplate("rated", "libraries", func(args RatingArgs) golem.Predicate[Media] {
			return Medias.AgeRating.IsNotNull().And(Medias.AgeRating.LTE(args.MaxAge), Medias.Adult.Eq(false))
		}),
		golem.FactTemplate("ratedOwner", "owner", func(args RatingArgs) golem.Predicate[Media] {
			return Medias.AgeRating.IsNotNull().And(Medias.AgeRating.LTE(args.MaxAge), Medias.Adult.Eq(false))
		}),
	))
}

func (Video) GolemModel() golem.ModelSpec[Video] {
	return golem.DefineModel(golem.Subscriptions(
		golem.ScopeTemplate("libraries", func(args LibraryDomainArgs) golem.Predicate[Video] {
			return Videos.MediaID.In(args.MediaIDs...).Or(Videos.StorageID.In(args.StorageIDs...), Videos.StorageObjects.Some(
				StorageObjects.StorageID.In(args.StorageIDs...).And(StorageObjects.State.In("PRESENT", "MISSING")),
			))
		}),
		golem.FactTemplate("fileGranted", "libraries", func(args VideoAccessArgs) golem.Predicate[Video] {
			return Videos.RetiredAt.IsNull().And(Videos.MediaID.In(args.AllowedMediaIDs...).Or(
				Videos.StorageID.In(args.GrantedStorageIDs...), Videos.StorageID.In(args.OpenStorageIDs...),
				Videos.StorageObjects.Some(StorageObjects.State.In("PRESENT", "MISSING").And(
					StorageObjects.StorageID.In(args.GrantedStorageIDs...).Or(StorageObjects.StorageID.In(args.OpenStorageIDs...)),
				)),
			))
		}),
	))
}

func (StorageObject) GolemModel() golem.ModelSpec[StorageObject] {
	return golem.DefineModel(golem.Subscriptions[StorageObject]())
}

func (Episode) GolemModel() golem.ModelSpec[Episode] {
	return golem.DefineModel(golem.Subscriptions[Episode]())
}

func (WatchProgress) GolemModel() golem.ModelSpec[WatchProgress] {
	return golem.DefineModel(golem.Subscriptions[WatchProgress]())
}

func (PlaylistVideo) GolemModel() golem.ModelSpec[PlaylistVideo] {
	return golem.DefineModel(golem.Subscriptions[PlaylistVideo]())
}

func (Media) DefinePolicy(rules *golem.Rules[Media], actor Actor) {
	var visible golem.Predicate[Media]
	var rated golem.Predicate[Media]
	maxAge := actor.MaxAge
	if actor.Guest && (!actor.Capped || maxAge > 17) {
		maxAge = 17
	}
	if actor.Owner {
		rules.BindFactScope(MediaScopes.Owner(OwnerCatalogArgs{ExcludedMediaIDs: actor.OwnerExcludedMediaIDs}))
		visible = rules.BindFact(MediaFacts.VisibleOwner(OwnerAccessArgs{DeniedStorageIDs: actor.DeniedStorageIDs, DeniedMediaIDs: actor.DeniedMediaIDs, RemovedMediaIDs: actor.RemovedMediaIDs}))
		if (actor.Capped || actor.Guest) && maxAge < 18 {
			rated = rules.BindFact(MediaFacts.RatedOwner(RatingArgs{MaxAge: maxAge}))
		}
	} else {
		rules.BindFactScope(MediaScopes.Libraries(LibraryDomainArgs{StorageIDs: actor.DomainStorageIDs, MediaIDs: actor.AllowedMediaIDs}))
		visible = rules.BindFact(MediaFacts.Visible(MediaAccessArgs{
			AllowedMediaIDs: actor.AllowedMediaIDs, GrantedStorageIDs: actor.GrantedStorageIDs,
			OpenStorageIDs: actor.OpenStorageIDs, RestrictedStorageIDs: actor.RestrictedStorageIDs,
			DeniedStorageIDs: actor.DeniedStorageIDs, DeniedMediaIDs: actor.DeniedMediaIDs, RemovedMediaIDs: actor.RemovedMediaIDs,
		}))
		if (actor.Capped || actor.Guest) && maxAge < 18 {
			rated = rules.BindFact(MediaFacts.Rated(RatingArgs{MaxAge: maxAge}))
		}
	}
	if actor.Capped || actor.Guest {
		if maxAge < 18 {
			visible = visible.And(rated)
		} else {
			visible = visible.And(Medias.AgeRating.IsNull().Or(Medias.AgeRating.LTE(maxAge)))
		}
	}
	rules.CanRead(visible)
	if actor.Owner {
		rules.CanCreate(golem.All[Media]())
		rules.CanUpdate(golem.All[Media]())
		rules.CanDelete(golem.All[Media]())
	}
}

func (Video) DefinePolicy(rules *golem.Rules[Video], actor Actor) {
	if actor.Owner {
		rules.CanRead(Videos.RetiredAt.IsNull().And(Videos.Media.Is(golem.All[Media]())))
		rules.CanCreate(golem.All[Video]())
		rules.CanUpdate(golem.All[Video]())
		rules.CanDelete(golem.All[Video]())
	} else {
		rules.BindFactScope(VideoScopes.Libraries(LibraryDomainArgs{StorageIDs: actor.DomainStorageIDs, MediaIDs: actor.AllowedMediaIDs}))
		file := rules.BindFact(VideoFacts.FileGranted(VideoAccessArgs{AllowedMediaIDs: actor.AllowedMediaIDs, GrantedStorageIDs: actor.GrantedStorageIDs, OpenStorageIDs: actor.OpenStorageIDs}))
		rules.CanRead(file.And(Videos.Media.Is(golem.All[Media]())))
	}
}

func (StorageObject) DefinePolicy(rules *golem.Rules[StorageObject], actor Actor) {
	if actor.ReadStorageObjects {
		rules.CanRead(StorageObjects.Video.Is(golem.All[Video]()))
	} else {
		rules.CanRead(golem.None[StorageObject]())
	}
	if actor.Owner {
		rules.CanCreate(golem.All[StorageObject]())
		rules.CanUpdate(golem.All[StorageObject]())
		rules.CanDelete(golem.All[StorageObject]())
	}
}

func (Episode) DefinePolicy(rules *golem.Rules[Episode], actor Actor) {
	rules.CanRead(Episodes.Video.Is(golem.All[Video]()))
	if actor.Owner {
		rules.CanCreate(golem.All[Episode]())
		rules.CanUpdate(golem.All[Episode]())
		rules.CanDelete(golem.All[Episode]())
	}
}

func (WatchProgress) DefinePolicy(rules *golem.Rules[WatchProgress], actor Actor) {
	owned := WatchProgresses.ProfileID.In(actor.OwnedProfileIDs...).And(WatchProgresses.Video.Is(golem.All[Video]()))
	rules.CanRead(owned)
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
	rules.CanDelete(owned)
}

func (PlaylistVideo) DefinePolicy(rules *golem.Rules[PlaylistVideo], actor Actor) {
	rules.CanRead(PlaylistVideos.PlaylistID.In(actor.VisiblePlaylistIDs...).And(PlaylistVideos.Video.Is(golem.All[Video]())))
	owned := PlaylistVideos.PlaylistID.In(actor.OwnedPlaylistIDs...).And(PlaylistVideos.Video.Is(golem.All[Video]()))
	rules.CanCreate(owned)
	rules.CanUpdate(owned)
	rules.CanDelete(owned)
}
