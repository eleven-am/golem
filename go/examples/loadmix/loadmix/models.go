package loadmix

import (
	"time"

	"github.com/eleven-am/golem/go/golem"
)

type SourceType string

const (
	SourceTypeURL     SourceType = "URL"
	SourceTypeYouTube SourceType = "YOUTUBE"
	SourceTypePDF     SourceType = "PDF"
	SourceTypeEPUB    SourceType = "EPUB"
)

func (SourceType) GolemEnum() golem.EnumSpec[SourceType] {
	return golem.DefineEnum(
		golem.EnumValue(SourceTypeURL),
		golem.EnumValue(SourceTypeYouTube),
		golem.EnumValue(SourceTypePDF),
		golem.EnumValue(SourceTypeEPUB),
	)
}

type ArticlePreference string

const (
	ArticlePreferenceNeutral ArticlePreference = "NEUTRAL"
	ArticlePreferenceLike    ArticlePreference = "LIKE"
	ArticlePreferenceDislike ArticlePreference = "DISLIKE"
)

func (ArticlePreference) GolemEnum() golem.EnumSpec[ArticlePreference] {
	return golem.DefineEnum(
		golem.EnumValue(ArticlePreferenceNeutral),
		golem.EnumValue(ArticlePreferenceLike),
		golem.EnumValue(ArticlePreferenceDislike),
	)
}

type ArticleStatus string

const (
	ArticleStatusPending    ArticleStatus = "PENDING"
	ArticleStatusExtracting ArticleStatus = "EXTRACTING"
	ArticleStatusReady      ArticleStatus = "READY"
	ArticleStatusFailed     ArticleStatus = "FAILED"
)

func (ArticleStatus) GolemEnum() golem.EnumSpec[ArticleStatus] {
	return golem.DefineEnum(
		golem.EnumValue(ArticleStatusPending),
		golem.EnumValue(ArticleStatusExtracting),
		golem.EnumValue(ArticleStatusReady),
		golem.EnumValue(ArticleStatusFailed),
	)
}

type FeatureStatus string

const (
	FeatureStatusIdle    FeatureStatus = "IDLE"
	FeatureStatusPending FeatureStatus = "PENDING"
	FeatureStatusRunning FeatureStatus = "RUNNING"
	FeatureStatusReady   FeatureStatus = "READY"
	FeatureStatusFailed  FeatureStatus = "FAILED"
)

func (FeatureStatus) GolemEnum() golem.EnumSpec[FeatureStatus] {
	return golem.DefineEnum(
		golem.EnumValue(FeatureStatusIdle),
		golem.EnumValue(FeatureStatusPending),
		golem.EnumValue(FeatureStatusRunning),
		golem.EnumValue(FeatureStatusReady),
		golem.EnumValue(FeatureStatusFailed),
	)
}

type ChatMessageStatus string

const (
	ChatMessageStatusPending   ChatMessageStatus = "PENDING"
	ChatMessageStatusStreaming ChatMessageStatus = "STREAMING"
	ChatMessageStatusCompleted ChatMessageStatus = "COMPLETED"
	ChatMessageStatusFailed    ChatMessageStatus = "FAILED"
	ChatMessageStatusCancelled ChatMessageStatus = "CANCELLED"
)

func (ChatMessageStatus) GolemEnum() golem.EnumSpec[ChatMessageStatus] {
	return golem.DefineEnum(
		golem.EnumValue(ChatMessageStatusPending),
		golem.EnumValue(ChatMessageStatusStreaming),
		golem.EnumValue(ChatMessageStatusCompleted),
		golem.EnumValue(ChatMessageStatusFailed),
		golem.EnumValue(ChatMessageStatusCancelled),
	)
}

type User struct {
	_ struct{} `golem:"model;id=loadmix.User;table=users"`

	ID        golem.UUID `db:"id" golem:"id=loadmix.User.ID;pk;default=uuid"`
	Email     string     `db:"email"`
	Name      string     `db:"name" golem:"default=\"\""`
	CreatedAt time.Time  `db:"created_at" golem:"default=now;readonly"`

	Articles    []Article    `db:"-" golem:"relation=has_many;fields=id;references=user_id"`
	ChatThreads []ChatThread `db:"-" golem:"relation=has_many;fields=id;references=user_id"`
}

type Article struct {
	_ struct{} `golem:"model;id=loadmix.Article;table=articles"`
	_ struct{} `golem:"unique=uq_articles_user_url(user_id,url)"`
	_ struct{} `golem:"index=idx_articles_user_status(user_id,status)"`
	_ struct{} `golem:"index=idx_articles_user_archived(user_id,is_archived)"`
	_ struct{} `golem:"index=idx_articles_user_preference(user_id,preference)"`
	_ struct{} `golem:"index=idx_articles_user_last_read(user_id,last_read_at)"`

	ID                    golem.UUID            `db:"id" golem:"id=loadmix.Article.ID;pk;default=uuid"`
	SourceType            SourceType            `db:"source_type" golem:"default=URL"`
	URL                   golem.Null[string]    `db:"url"`
	Title                 golem.Null[string]    `db:"title"`
	Author                golem.Null[string]    `db:"author"`
	SiteName              golem.Null[string]    `db:"site_name"`
	PublishedDate         golem.Null[time.Time] `db:"published_date"`
	MarkdownContent       golem.Null[string]    `db:"markdown_content"`
	PlainTextContent      golem.Null[string]    `db:"plain_text_content"`
	ScreenshotPath        golem.Null[string]    `db:"screenshot_path"`
	CoverImagePath        golem.Null[string]    `db:"cover_image_path"`
	HeroImageURL          golem.Null[string]    `db:"hero_image_url"`
	OriginalLanguage      golem.Null[string]    `db:"original_language"`
	Summary               golem.Null[string]    `db:"summary"`
	SuggestedPreamble     golem.Null[string]    `db:"suggested_preamble"`
	RoleplayCharacter     golem.Null[string]    `db:"roleplay_character"`
	RoleplayUserCharacter golem.Null[string]    `db:"roleplay_user_character"`
	RoleplayScene         golem.Null[string]    `db:"roleplay_scene"`
	WordCount             golem.Null[int32]     `db:"word_count"`
	ReadingTimeMinutes    golem.Null[int32]     `db:"reading_time_minutes"`
	ContentHash           golem.Null[string]    `db:"content_hash"`
	Status                ArticleStatus         `db:"status" golem:"default=PENDING"`
	ExtractError          golem.Null[string]    `db:"extract_error"`
	ScreenshotStatus      FeatureStatus         `db:"screenshot_status" golem:"default=IDLE"`
	AnalysisStatus        FeatureStatus         `db:"analysis_status" golem:"default=IDLE"`
	PreambleStatus        FeatureStatus         `db:"preamble_status" golem:"default=IDLE"`
	EmbeddingStatus       FeatureStatus         `db:"embedding_status" golem:"default=IDLE"`
	AudioStatus           FeatureStatus         `db:"audio_status" golem:"default=IDLE"`
	AudioFilePath         golem.Null[string]    `db:"audio_file_path"`
	AudioVoiceUsed        golem.Null[string]    `db:"audio_voice_used"`
	AudioError            golem.Null[string]    `db:"audio_error"`
	AudioProgress         float64               `db:"audio_progress" golem:"default=0"`
	LastReadAt            golem.Null[time.Time] `db:"last_read_at"`
	ReadingProgress       float64               `db:"reading_progress" golem:"default=0"`
	IsArchived            bool                  `db:"is_archived" golem:"default=false"`
	Preference            ArticlePreference     `db:"preference" golem:"default=NEUTRAL"`
	CreatedAt             time.Time             `db:"created_at" golem:"default=now;readonly"`
	UpdatedAt             time.Time             `db:"updated_at" golem:"updated;readonly"`

	UserID        golem.UUID       `db:"user_id" golem:"immutable"`
	User          *User            `db:"-" golem:"relation=belongs_to;fields=user_id;references=id;immutable"`
	Sessions      []ReadingSession `db:"-" golem:"relation=has_many;fields=id;references=article_id"`
	Thread        *ChatThread      `db:"-" golem:"relation=has_one;fields=id;references=article_id"`
	AudioArtifact *ArticleAudio    `db:"-" golem:"relation=has_one;fields=id;references=article_id"`
}

func (Article) GolemModel() golem.ModelSpec[Article] {
	return golem.DefineModel(
		golem.Subscriptions[Article](),
		golem.RelationOptions(Articles.User).OnDelete(golem.Cascade),
	)
}

type ArticleAudio struct {
	_ struct{} `golem:"model;id=loadmix.ArticleAudio;table=article_audio"`
	_ struct{} `golem:"unique=uq_article_audio_article(article_id)"`
	_ struct{} `golem:"index=idx_article_audio_user_status(user_id,generation_status)"`

	ID                   golem.UUID            `db:"id" golem:"id=loadmix.ArticleAudio.ID;pk;default=uuid"`
	GenerationStatus     FeatureStatus         `db:"generation_status" golem:"default=PENDING"`
	SourceContentHash    string                `db:"source_content_hash"`
	Voice                string                `db:"voice"`
	TTSModel             string                `db:"tts_model"`
	ContentType          string                `db:"content_type" golem:"default=\"audio/mpeg\""`
	StoragePath          golem.Null[string]    `db:"storage_path"`
	SpecificationVersion string                `db:"specification_version" golem:"default=\"readable-audio-v1\""`
	GenerationError      golem.Null[string]    `db:"generation_error"`
	GeneratedAt          golem.Null[time.Time] `db:"generated_at"`
	CreatedAt            time.Time             `db:"created_at" golem:"default=now;readonly"`
	UpdatedAt            time.Time             `db:"updated_at" golem:"updated;readonly"`
	UserID               golem.UUID            `db:"user_id" golem:"immutable"`
	User                 *User                 `db:"-" golem:"relation=belongs_to;fields=user_id;references=id;immutable"`
	ArticleID            golem.UUID            `db:"article_id" golem:"immutable"`
	Article              *Article              `db:"-" golem:"relation=belongs_to;fields=article_id;references=id;immutable"`
}

func (ArticleAudio) GolemModel() golem.ModelSpec[ArticleAudio] {
	return golem.DefineModel(
		golem.Subscriptions[ArticleAudio](),
		golem.RelationOptions(ArticleAudios.User).OnDelete(golem.Cascade),
		golem.RelationOptions(ArticleAudios.Article).OnDelete(golem.Cascade),
	)
}

type ReadingSession struct {
	_ struct{} `golem:"model;id=loadmix.ReadingSession;table=reading_sessions"`

	ID            golem.UUID            `db:"id" golem:"id=loadmix.ReadingSession.ID;pk;default=uuid"`
	ArticleID     golem.UUID            `db:"article_id"`
	StartedAt     time.Time             `db:"started_at" golem:"default=now"`
	EndedAt       golem.Null[time.Time] `db:"ended_at"`
	DurationSec   int32                 `db:"duration_sec" golem:"default=0"`
	ProgressStart float64               `db:"progress_start" golem:"default=0"`
	ProgressEnd   float64               `db:"progress_end" golem:"default=0"`
	SessionType   string                `db:"session_type" golem:"default=\"reading\""`
	Article       *Article              `db:"-" golem:"relation=belongs_to;fields=article_id;references=id"`
}

func (ReadingSession) GolemModel() golem.ModelSpec[ReadingSession] {
	return golem.DefineModel(
		golem.RelationOptions(ReadingSessions.Article).OnDelete(golem.Cascade),
	)
}

type ChatThread struct {
	_ struct{} `golem:"model;id=loadmix.ChatThread;table=chat_threads"`
	_ struct{} `golem:"unique=uq_chat_threads_article(article_id)"`

	ID        golem.UUID         `db:"id" golem:"id=loadmix.ChatThread.ID;pk;default=uuid"`
	Preamble  golem.Null[string] `db:"preamble"`
	ArticleID golem.UUID         `db:"article_id"`
	UserID    golem.UUID         `db:"user_id" golem:"immutable"`
	CreatedAt time.Time          `db:"created_at" golem:"default=now;readonly"`
	Article   *Article           `db:"-" golem:"relation=belongs_to;fields=article_id;references=id"`
	User      *User              `db:"-" golem:"relation=belongs_to;fields=user_id;references=id;immutable"`
	Messages  []ChatMessage      `db:"-" golem:"relation=has_many;fields=id;references=thread_id"`
}

func (ChatThread) GolemModel() golem.ModelSpec[ChatThread] {
	return golem.DefineModel(
		golem.Subscriptions[ChatThread](),
		golem.RelationOptions(ChatThreads.Article).OnDelete(golem.Cascade),
		golem.RelationOptions(ChatThreads.User).OnDelete(golem.Cascade),
	)
}

type ChatMessage struct {
	_ struct{} `golem:"model;id=loadmix.ChatMessage;table=chat_messages"`
	_ struct{} `golem:"index=idx_chat_messages_thread_created(thread_id,created_at)"`

	ID          golem.UUID            `db:"id" golem:"id=loadmix.ChatMessage.ID;pk;default=uuid"`
	Role        string                `db:"role"`
	Content     string                `db:"content"`
	Status      ChatMessageStatus     `db:"status" golem:"default=COMPLETED"`
	Error       golem.Null[string]    `db:"error"`
	CompletedAt golem.Null[time.Time] `db:"completed_at"`
	ThreadID    golem.UUID            `db:"thread_id"`
	CreatedAt   time.Time             `db:"created_at" golem:"default=now;readonly"`
	Thread      *ChatThread           `db:"-" golem:"relation=belongs_to;fields=thread_id;references=id"`
}

func (ChatMessage) GolemModel() golem.ModelSpec[ChatMessage] {
	return golem.DefineModel(
		golem.Subscriptions[ChatMessage](),
		golem.RelationOptions(ChatMessages.Thread).OnDelete(golem.Cascade),
	)
}
