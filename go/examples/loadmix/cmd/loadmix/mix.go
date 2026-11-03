package main

import (
	"context"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/eleven-am/golem/go/examples/loadmix/loadmix"
	"github.com/eleven-am/golem/go/golem"
)

const (
	readerRate        = 1.43
	listenerSlowRate  = 1.0 / 30
	listenerFastRate  = 1.0 / 15
	seekRate          = 1.0 / 300
	sessionRate       = 1.0 / 300
	chatCreateRate    = 1.0 / 20
	chatCheckpoint    = 0.75
	saveInterval      = 120.0
	pipelineSteps     = 12
	pipelineSpacing   = 5 * time.Second
	scrubUsers        = 40
	scrubRate         = 4
	scrubDuration     = 10 * time.Second
	playUsers         = 25
	regenerateRows    = 100
	regenerateUpdates = 2
	regenerateDelay   = 5 * time.Second
	burstDuration     = 10 * time.Second
	chatCheckpointLen = 2048
)

var checkpointText = strings.Repeat("x", chatCheckpointLen)

func (harness *Harness) articleUpdate(user *simUser, article golem.UUID, input loadmix.ArticleUpdateInput) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := user.caller.Articles.Update(ctx, loadmix.Articles.ByID.Value(article), input)
		if err == nil {
			user.articles.expected.Add(1)
		}
		return err
	}
}

func (harness *Harness) readProgress(user *simUser) func(context.Context) error {
	value := float64(user.counter.Add(1)%1000) / 1000
	return harness.articleUpdate(user, user.article, loadmix.Articles.Update(loadmix.Articles.ReadingProgress.Set(value), loadmix.Articles.LastReadAt.Set(time.Now())))
}

func (harness *Harness) audioProgress(user *simUser) func(context.Context) error {
	value := float64(user.counter.Add(1)%1000) / 1000
	return harness.articleUpdate(user, user.article, loadmix.Articles.Update(loadmix.Articles.AudioProgress.Set(value)))
}

func (harness *Harness) sessionStart(user *simUser, id golem.UUID) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := user.caller.ReadingSessions.Create(ctx, loadmix.ReadingSessions.Create(loadmix.ReadingSessions.ID.Create(id), loadmix.ReadingSessions.ArticleID.Create(user.article), loadmix.ReadingSessions.SessionType.Create("listening")))
		return err
	}
}

func (harness *Harness) sessionEnd(user *simUser, id golem.UUID) func(context.Context) error {
	return func(ctx context.Context) error {
		_, err := user.caller.ReadingSessions.Update(ctx, loadmix.ReadingSessions.ByID.Value(id), loadmix.ReadingSessions.Update(loadmix.ReadingSessions.DurationSec.Set(300), loadmix.ReadingSessions.EndedAt.Set(time.Now())))
		return err
	}
}

func (harness *Harness) chatCreate(user *simUser) func(context.Context) error {
	id := harness.nextID(9)
	harness.tracker.register(id, user.id)
	return func(ctx context.Context) error {
		_, err := user.caller.ChatMessages.Create(ctx, loadmix.ChatMessages.Create(loadmix.ChatMessages.ID.Create(id), loadmix.ChatMessages.ThreadID.Create(user.thread), loadmix.ChatMessages.Role.Create("assistant"), loadmix.ChatMessages.Content.Create(""), loadmix.ChatMessages.Status.Create(loadmix.ChatMessageStatusStreaming)))
		if err == nil {
			user.message.Store(id)
			user.messages.expected.Add(1)
		}
		return err
	}
}

func (harness *Harness) chatCheckpoint(user *simUser) func(context.Context) error {
	message := user.message.Load().(golem.UUID)
	length := 256 * (1 + int(user.counter.Add(1)%8))
	return func(ctx context.Context) error {
		_, err := user.caller.ChatMessages.Update(ctx, loadmix.ChatMessages.ByID.Value(message), loadmix.ChatMessages.Update(loadmix.ChatMessages.Content.Set(checkpointText[:length])))
		if err == nil {
			user.messages.expected.Add(1)
		}
		return err
	}
}

func (harness *Harness) pipelineCreate(user *simUser, article golem.UUID) func(context.Context) error {
	harness.tracker.register(article, user.id)
	return func(ctx context.Context) error {
		_, err := user.caller.Articles.Create(ctx, loadmix.Articles.Create(loadmix.Articles.ID.Create(article), loadmix.Articles.UserID.Create(user.id), loadmix.Articles.URL.Create("https://loadmix.test/"+article.String()), loadmix.Articles.Title.Create("saved")))
		if err == nil {
			user.articles.expected.Add(1)
		}
		return err
	}
}

var pipelineStatuses = []loadmix.FeatureStatus{loadmix.FeatureStatusPending, loadmix.FeatureStatusRunning, loadmix.FeatureStatusReady}

func (harness *Harness) pipelineArticleStep(user *simUser, article golem.UUID, step int) func(context.Context) error {
	status := pipelineStatuses[step%len(pipelineStatuses)]
	return harness.articleUpdate(user, article, loadmix.Articles.Update(loadmix.Articles.AnalysisStatus.Set(status), loadmix.Articles.EmbeddingStatus.Set(status)))
}

func (harness *Harness) audioUpdate(caller *loadmix.Caller[loadmix.Principal], audio golem.UUID, step int) func(context.Context) error {
	status := pipelineStatuses[step%len(pipelineStatuses)]
	return func(ctx context.Context) error {
		_, err := caller.ArticleAudios.Update(ctx, loadmix.ArticleAudios.ByID.Value(audio), loadmix.ArticleAudios.Update(loadmix.ArticleAudios.GenerationStatus.Set(status)))
		return err
	}
}

func (harness *Harness) nextID(namespace byte) golem.UUID {
	return uuidFrom(namespace, int(harness.sequence.Add(1)))
}

func (harness *Harness) startSteady(users int, seed int64) {
	pick := func(list []*simUser, rng *rand.Rand) *simUser { return list[rng.Intn(len(list))] }
	readers, listeners, chatters := harness.readers, harness.listeners, harness.chatters
	harness.poisson(float64(len(readers))*readerRate, seed+1, func(rng *rand.Rand, at time.Duration) {
		harness.submit(ClassArticle, at, harness.readProgress(pick(readers, rng)))
	})
	slow := listeners[:len(listeners)/2]
	fast := listeners[len(listeners)/2:]
	if len(slow) != 0 {
		harness.poisson(float64(len(slow))*listenerSlowRate, seed+2, func(rng *rand.Rand, at time.Duration) {
			harness.submit(ClassArticle, at, harness.audioProgress(pick(slow, rng)))
		})
	}
	if len(fast) != 0 {
		harness.poisson(float64(len(fast))*listenerFastRate, seed+3, func(rng *rand.Rand, at time.Duration) {
			harness.submit(ClassArticle, at, harness.audioProgress(pick(fast, rng)))
		})
	}
	if len(listeners) != 0 {
		harness.poisson(float64(len(listeners))*seekRate, seed+4, func(rng *rand.Rand, at time.Duration) {
			harness.submit(ClassArticle, at, harness.audioProgress(pick(listeners, rng)))
		})
		harness.poisson(float64(len(listeners))*sessionRate, seed+5, func(rng *rand.Rand, at time.Duration) {
			user := pick(listeners, rng)
			id := harness.nextID(8)
			harness.submit(ClassControl, at, harness.sessionStart(user, id))
			harness.at(at+2*time.Second, func(later time.Duration) {
				harness.submit(ClassControl, later, harness.sessionEnd(user, id))
			})
		})
	}
	if len(chatters) != 0 {
		harness.poisson(float64(len(chatters))*chatCreateRate, seed+6, func(rng *rand.Rand, at time.Duration) {
			harness.submit(ClassChatMessage, at, harness.chatCreate(pick(chatters, rng)))
		})
		harness.poisson(float64(len(chatters))*chatCheckpoint, seed+7, func(rng *rand.Rand, at time.Duration) {
			harness.submit(ClassChatMessage, at, harness.chatCheckpoint(pick(chatters, rng)))
		})
	}
	harness.poisson(float64(users)/saveInterval, seed+8, func(rng *rand.Rand, at time.Duration) {
		user := pick(harness.users, rng)
		article := harness.nextID(10)
		harness.submit(ClassArticle, at, harness.pipelineCreate(user, article))
		for step := 1; step <= pipelineSteps; step++ {
			step := step
			harness.at(at+time.Duration(step)*pipelineSpacing, func(later time.Duration) {
				if step%4 == 0 {
					harness.submit(ClassArticleAudio, later, harness.audioUpdate(user.caller, user.audio, step))
					return
				}
				harness.submit(ClassArticle, later, harness.pipelineArticleStep(user, article, step))
			})
		}
	})
}

func (harness *Harness) scheduleBurst(start time.Duration) error {
	if len(harness.users) < len(harness.readers)+scrubUsers || len(harness.regen) < regenerateRows {
		return fmt.Errorf("burst profile needs %d scrub-capable users beyond the readers and %d regeneration rows", scrubUsers, regenerateRows)
	}
	scrubbers := harness.users[len(harness.readers) : len(harness.readers)+scrubUsers]
	interval := time.Second / scrubRate
	for index, user := range scrubbers {
		user := user
		phase := time.Duration(index) * interval / time.Duration(len(scrubbers))
		for write := 0; write < int(scrubDuration/interval); write++ {
			harness.at(start+phase+time.Duration(write)*interval, func(at time.Duration) {
				harness.submit(ClassArticle, at, harness.audioProgress(user))
			})
		}
	}
	players := harness.users[len(harness.readers) : len(harness.readers)+playUsers]
	for index, user := range players {
		user := user
		offset := time.Duration(index) * time.Second / time.Duration(len(players))
		harness.at(start+offset, func(at time.Duration) {
			harness.submit(ClassControl, at, harness.sessionStart(user, harness.nextID(8)))
			harness.submit(ClassArticle, at, harness.audioProgress(user))
		})
	}
	for index, target := range harness.regen[:regenerateRows] {
		target := target
		for update := 0; update < regenerateUpdates; update++ {
			update := update
			offset := regenerateDelay + time.Duration((index*regenerateUpdates+update)*int(time.Second)/(regenerateRows*regenerateUpdates))
			harness.at(start+offset, func(at time.Duration) {
				harness.submit(ClassArticleAudio, at, harness.audioUpdate(target.caller, target.audio, update))
			})
		}
	}
	return nil
}

func (harness *Harness) startSaturation(writers int) {
	for index := 0; index < writers; index++ {
		user := harness.users[index]
		harness.background.Add(1)
		go func() {
			defer harness.background.Done()
			for harness.ctx.Err() == nil {
				at := time.Since(harness.origin)
				harness.arrive(ClassArticle, at)
				harness.perform(ClassArticle, at, harness.audioProgress(user))
			}
		}()
	}
}
