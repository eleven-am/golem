package runtime

import (
	"container/list"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	policyruntime "github.com/eleven-am/golem/go/internal/policy/runtime"
)

type PolicyCacheConfig struct {
	Disabled  bool
	MaxActors int
	TTL       time.Duration
}

const defaultPolicyCacheMaxActors = 1024

type policyCache struct {
	mu      sync.Mutex
	limit   int
	ttl     time.Duration
	now     func() time.Time
	epoch   atomic.Uint64
	order   *list.List
	entries map[string]*list.Element
}

type policyCacheEntry struct {
	key     string
	created time.Time
	once    sync.Once
	set     *policyruntime.Set
	err     error
}

func newPolicyCache(config PolicyCacheConfig) (*policyCache, error) {
	if config.MaxActors < 0 {
		return nil, fmt.Errorf("P3_RUNTIME_CONFIG: policy cache MaxActors must not be negative")
	}
	if config.TTL < 0 {
		return nil, fmt.Errorf("P3_RUNTIME_CONFIG: policy cache TTL must not be negative")
	}
	if config.Disabled {
		return nil, nil
	}
	limit := config.MaxActors
	if limit == 0 {
		limit = defaultPolicyCacheMaxActors
	}
	return &policyCache{limit: limit, ttl: config.TTL, now: time.Now, order: list.New(), entries: make(map[string]*list.Element)}, nil
}

func (cache *policyCache) get(key string, build func() (*policyruntime.Set, error)) (*policyruntime.Set, error) {
	key = string(binary.BigEndian.AppendUint64(nil, cache.epoch.Load())) + key
	entry := cache.entry(key)
	entry.once.Do(func() {
		entry.set, entry.err = build()
	})
	if entry.err != nil {
		cache.remove(entry)
		return nil, entry.err
	}
	return entry.set, nil
}

func (cache *policyCache) entry(key string) *policyCacheEntry {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	now := cache.now()
	if element, ok := cache.entries[key]; ok {
		entry := element.Value.(*policyCacheEntry)
		if cache.ttl <= 0 || now.Sub(entry.created) < cache.ttl {
			cache.order.MoveToFront(element)
			return entry
		}
		cache.order.Remove(element)
		delete(cache.entries, key)
	}
	entry := &policyCacheEntry{key: key, created: now}
	cache.entries[key] = cache.order.PushFront(entry)
	for cache.order.Len() > cache.limit {
		oldest := cache.order.Back()
		cache.order.Remove(oldest)
		delete(cache.entries, oldest.Value.(*policyCacheEntry).key)
	}
	return entry
}

func (cache *policyCache) remove(entry *policyCacheEntry) {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if element, ok := cache.entries[entry.key]; ok && element.Value.(*policyCacheEntry) == entry {
		cache.order.Remove(element)
		delete(cache.entries, entry.key)
	}
}

func (cache *policyCache) invalidate() {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	cache.epoch.Add(1)
	cache.order.Init()
	cache.entries = make(map[string]*list.Element)
}

func (cache *policyCache) len() int {
	cache.mu.Lock()
	defer cache.mu.Unlock()
	return cache.order.Len()
}

type policyTypeTable struct {
	mu    sync.Mutex
	types map[reflect.Type]uint64
}

var policyKeyTypes = policyTypeTable{types: make(map[reflect.Type]uint64)}

func (table *policyTypeTable) identity(typ reflect.Type) uint64 {
	table.mu.Lock()
	defer table.mu.Unlock()
	if identity, ok := table.types[typ]; ok {
		return identity
	}
	identity := uint64(len(table.types) + 1)
	table.types[typ] = identity
	return identity
}

func policyCacheKey[A any](actor A, snapshot func(A) (A, error), custom func(A) (string, bool)) (string, bool) {
	if custom != nil {
		key, ok := custom(actor)
		if !ok {
			return "", false
		}
		return "k" + key, true
	}
	if snapshot != nil {
		return "", false
	}
	encoded, ok := appendActorKey([]byte{'v'}, reflect.ValueOf(&actor).Elem())
	if !ok {
		return "", false
	}
	return string(encoded), true
}

func appendActorKey(buffer []byte, value reflect.Value) ([]byte, bool) {
	buffer = binary.AppendUvarint(buffer, policyKeyTypes.identity(value.Type()))
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return append(buffer, 0), true
		}
		return appendActorKey(append(buffer, 1), value.Elem())
	case reflect.Struct:
		for index := 0; index < value.NumField(); index++ {
			var ok bool
			if buffer, ok = appendActorKey(buffer, value.Field(index)); !ok {
				return nil, false
			}
		}
		return buffer, true
	case reflect.Array:
		for index := 0; index < value.Len(); index++ {
			var ok bool
			if buffer, ok = appendActorKey(buffer, value.Index(index)); !ok {
				return nil, false
			}
		}
		return buffer, true
	case reflect.Bool:
		if value.Bool() {
			return append(buffer, 1), true
		}
		return append(buffer, 0), true
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return binary.BigEndian.AppendUint64(buffer, uint64(value.Int())), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return binary.BigEndian.AppendUint64(buffer, value.Uint()), true
	case reflect.Float32:
		return binary.BigEndian.AppendUint32(buffer, math.Float32bits(float32(value.Float()))), true
	case reflect.Float64:
		return binary.BigEndian.AppendUint64(buffer, math.Float64bits(value.Float())), true
	case reflect.Complex64:
		number := value.Complex()
		buffer = binary.BigEndian.AppendUint32(buffer, math.Float32bits(float32(real(number))))
		return binary.BigEndian.AppendUint32(buffer, math.Float32bits(float32(imag(number)))), true
	case reflect.Complex128:
		number := value.Complex()
		buffer = binary.BigEndian.AppendUint64(buffer, math.Float64bits(real(number)))
		return binary.BigEndian.AppendUint64(buffer, math.Float64bits(imag(number))), true
	case reflect.String:
		text := value.String()
		buffer = binary.AppendUvarint(buffer, uint64(len(text)))
		return append(buffer, text...), true
	default:
		return nil, false
	}
}
