# Vision Request Preprocessing Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 在 Anthropic Messages 主请求转发前，用同一 provider 的 `sonnet` 影子模型并发识别图片，并以详细文本描述替换图片块。

**Architecture:** 在现有 proxy handler 前增加独立的 `internal/vision` 预处理器。该包拆分为 Anthropic JSON 改写、TTL/LRU 合并缓存、影子模型 HTTP 客户端和并发编排四个职责；现有主请求重试、响应流和统计流程保持原位。

**Tech Stack:** Go 1.25、标准库 `net/http`/`encoding/json`/`container/list`/`crypto/sha256`、现有 `gopkg.in/yaml.v3`、现有 SQLite 统计模块、`httptest`、Docker。

## Global Constraints

- 首版只处理 `POST /v1/messages` 且 `Content-Type` 为 `application/json` 的 Anthropic 请求。
- 支持 `base64`、URL 和 Files API `file_id` 三种图片源；不处理 `tool_result.content` 内嵌图片。
- 每张图片使用独立非流式 `sonnet` 子请求；主请求中不保留图片块。
- 任一图片失败即返回错误，主模型调用次数必须为零。
- 缓存是进程内 TTL + LRU，默认 30 分钟、512 项；全局识图并发默认 4。
- 禁用功能或请求不含图片时，现有请求字节和代理行为保持不变。
- 不新增第三方 Go 依赖。
- 测试、构建和 race 检查使用 Go 1.25 Docker 容器。
- 保留 `config.yaml` 中本地已有的 `body_contains: "未找到"` 重试规则。
- 不执行 `git add`、`git commit` 或 `git push`；每个任务以 diff 检查点结束。

---

## File Map

| 文件 | 职责 |
|---|---|
| `internal/config/config.go` | 解析、默认化并校验 provider 级 `vision` 配置 |
| `internal/config/config_test.go` | Vision 配置测试 |
| `internal/vision/message.go` | 保留未知字段地解析、校验和改写 Anthropic Messages JSON |
| `internal/vision/message_test.go` | 三类图片源、顺序、未知字段和响应文本解析测试 |
| `internal/vision/cache.go` | 线程安全 TTL/LRU 缓存和同键并发调用合并 |
| `internal/vision/cache_test.go` | 过期、淘汰、合并及取消测试 |
| `internal/vision/client.go` | 构造识图请求、复制允许头、执行重试、解析响应、记录 usage |
| `internal/vision/client_test.go` | 请求结构、头、重试、超时、响应和 usage 测试 |
| `internal/vision/preprocessor.go` | 多图并发编排、缓存接入、全有或全无改写及错误分类 |
| `internal/vision/preprocessor_test.go` | 无图直通、多图顺序、并发上限、失败及缓存测试 |
| `internal/proxy/proxy.go` | 把预处理器接入现有请求转发链路 |
| `internal/proxy/proxy_test.go` | Handler 级端到端测试 |
| `config.yaml` | 提供默认关闭的 provider 示例配置，保留本地重试规则 |
| `README.md` | 记录启用方式、数据流、限制及字段含义 |

---

### Task 1: Provider 级 Vision 配置

**Files:**

- Modify: `internal/config/config.go:20-156`
- Create: `internal/config/config_test.go`

**Interfaces:**

- Produces:
  - `config.VisionConfig`
  - `config.Config.Vision VisionConfig`
  - `providerYAML.Vision visionYAML`
- Consumes: 现有 `yamlDuration` 和 `resolve(...)`

- [ ] **Step 1: 写出默认值与校验的失败测试**

在 `internal/config/config_test.go` 使用 `package config`，直接测试 `resolve`：

```go
package config

import (
	"strings"
	"testing"
	"time"
)

func testProvider() providerYAML {
	return providerYAML{
		Upstream: "https://example.test/anthropic",
		OverloadRules: []ruleYAML{
			{Status: 529},
		},
	}
}

func TestResolveVisionDefaults(t *testing.T) {
	pc := testProvider()
	pc.Vision.Enabled = true

	got, err := resolve("test", pc, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Vision.Model != "sonnet" ||
		got.Vision.MaxTokens != 2048 ||
		got.Vision.Timeout != 2*time.Minute ||
		got.Vision.MaxConcurrency != 4 ||
		got.Vision.CacheTTL != 30*time.Minute ||
		got.Vision.CacheMaxEntries != 512 {
		t.Fatalf("unexpected defaults: %+v", got.Vision)
	}
}

func TestResolveVisionDisabledByDefault(t *testing.T) {
	got, err := resolve("test", testProvider(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Vision.Enabled {
		t.Fatal("vision must be disabled by default")
	}
}

func TestResolveVisionRequiresAnthropic(t *testing.T) {
	pc := testProvider()
	pc.Protocol = "openai"
	pc.Vision.Enabled = true

	_, err := resolve("test", pc, "", "")
	if err == nil || !strings.Contains(err.Error(), "vision requires protocol anthropic") {
		t.Fatalf("unexpected error: %v", err)
	}
}
```

增加表驱动测试，分别显式设置 `max_tokens: 0`、`timeout: 0s`、`max_concurrency: 0`、`cache_ttl: 0s` 和 `cache_max_entries: 0`，断言启用时返回包含具体字段名的错误。另加一个显式值测试，确认非默认的 model、prompt、token、timeout、并发和缓存字段全部进入运行时配置。

- [ ] **Step 2: 在 Docker 中验证测试先失败**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  go test ./internal/config -run Vision -count=1
```

Expected: 编译失败，提示 `Vision`、`VisionConfig` 或 `visionYAML` 尚未定义。

- [ ] **Step 3: 增加运行时与 YAML 配置类型**

在 `internal/config/config.go` 增加：

```go
const (
	defaultVisionModel           = "sonnet"
	defaultVisionMaxTokens       = 2048
	defaultVisionTimeout         = 2 * time.Minute
	defaultVisionMaxConcurrency  = 4
	defaultVisionCacheTTL        = 30 * time.Minute
	defaultVisionCacheMaxEntries = 512
)

type VisionConfig struct {
	Enabled         bool
	Model           string
	MaxTokens       int
	Timeout         time.Duration
	MaxConcurrency  int
	CacheTTL        time.Duration
	CacheMaxEntries int
	Prompt          string
}

type visionYAML struct {
	Enabled         bool          `yaml:"enabled"`
	Model           string        `yaml:"model"`
	MaxTokens       *int          `yaml:"max_tokens"`
	Timeout         *yamlDuration `yaml:"timeout"`
	MaxConcurrency  *int          `yaml:"max_concurrency"`
	CacheTTL        *yamlDuration `yaml:"cache_ttl"`
	CacheMaxEntries *int          `yaml:"cache_max_entries"`
	Prompt          string        `yaml:"prompt"`
}
```

给 `Config` 增加 `Vision VisionConfig`，给 `providerYAML` 增加 `Vision visionYAML`。

- [ ] **Step 4: 实现默认化与启用时校验**

在 `resolve` 确定 `protocol` 后调用：

```go
visionCfg, err := resolveVision(name, protocol, pc.Vision)
if err != nil {
	return nil, err
}
```

实现 `resolveVision`，先填入全部默认值，再覆盖非 nil YAML 字段；仅当 `Enabled` 为 true 时检查 Anthropic 协议及所有正值字段。错误格式固定为：

```go
fmt.Errorf("provider %q: vision requires protocol anthropic", name)
fmt.Errorf("provider %q: vision.max_tokens must be positive", name)
```

其他字段沿用相同格式，最后把 `visionCfg` 写入返回的 `Config`。

- [ ] **Step 5: 运行配置测试与格式化**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  sh -lc 'gofmt -w internal/config/config.go internal/config/config_test.go && go test ./internal/config -count=1'
```

Expected: `ok anthropic-proxy/internal/config`。

- [ ] **Step 6: 检查任务 diff，不提交**

Run:

```sh
git diff --check
git diff -- internal/config/config.go internal/config/config_test.go
```

确认只包含 Vision 配置及测试，不执行 Git 提交。

---

### Task 2: Anthropic 图片块解析与改写

**Files:**

- Create: `internal/vision/message.go`
- Create: `internal/vision/message_test.go`

**Interfaces:**

- Produces:
  - `type imageRef struct`
  - `func parseMessages(body []byte) (*messageDocument, error)`
  - `func (d *messageDocument) images() []imageRef`
  - `func (d *messageDocument) rewrite(descriptions []string) ([]byte, error)`
  - `func parseDescription(body []byte) (string, error)`
  - `func imageCacheKey(provider, model, prompt string, image imageRef) string`
- Consumes: 无

- [ ] **Step 1: 写出三类图片与顺序保持测试**

创建 `internal/vision/message_test.go`，用一个请求同时包含 text、base64、URL、file 和未知字段：

```go
func TestParseAndRewriteImages(t *testing.T) {
	body := []byte(`{
	  "model":"main-model",
	  "metadata":{"user_id":"u-1"},
	  "messages":[{"role":"user","custom":"keep","content":[
	    {"type":"text","text":"before"},
	    {"type":"image","source":{"type":"base64","media_type":"image/png","data":"aW1n"}},
	    {"type":"image","source":{"type":"url","url":"https://example.test/a.png"}},
	    {"type":"image","source":{"type":"file","file_id":"file_123"}},
	    {"type":"text","text":"after"}
	  ]}]
	}`)

	doc, err := parseMessages(body)
	if err != nil {
		t.Fatal(err)
	}
	images := doc.images()
	if len(images) != 3 {
		t.Fatalf("images=%d", len(images))
	}
	if images[0].sourceType != "base64" ||
		images[1].sourceType != "url" ||
		images[2].sourceType != "file" {
		t.Fatalf("wrong order: %#v", images)
	}

	rewritten, err := doc.rewrite([]string{"base64 desc", "url desc", "file desc"})
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(rewritten, &got); err != nil {
		t.Fatal(err)
	}
	if got["metadata"].(map[string]any)["user_id"] != "u-1" {
		t.Fatal("unknown top-level field was lost")
	}
	blocks := got["messages"].([]any)[0].(map[string]any)["content"].([]any)
	if len(blocks) != 5 {
		t.Fatalf("blocks=%d", len(blocks))
	}
	for i, want := range []string{"base64 desc", "url desc", "file desc"} {
		text := blocks[i+1].(map[string]any)["text"].(string)
		if text != descriptionPrefix+want {
			t.Fatalf("block %d text=%q", i+1, text)
		}
	}
}
```

另加测试覆盖：

- 字符串 `content` 和纯文本数组不产生图片；
- `source.type` 未知返回错误；
- base64 缺 `media_type`/`data`、URL 缺 `url`、file 缺 `file_id` 返回错误；
- `rewrite` 描述数量不匹配返回错误；
- 非流式响应的多个 text block 按换行连接，空 text 响应报错；
- 相同图片参数得到相同缓存键，不同 provider/model/prompt/source 得到不同键。

- [ ] **Step 2: 在 Docker 中验证测试先失败**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  go test ./internal/vision -run 'Parse|Rewrite|Description|CacheKey' -count=1
```

Expected: 编译失败，因为 `internal/vision` 实现尚不存在。

- [ ] **Step 3: 定义保留未知字段的文档结构**

在 `message.go` 定义：

```go
const descriptionPrefix = "[Image description generated by vision proxy]\n"

type imageRef struct {
	block        json.RawMessage
	sourceType   string
	cachePayload string
	messageIndex int
	blockIndex   int
}

type messageNode struct {
	fields       map[string]json.RawMessage
	content      []json.RawMessage
	arrayContent bool
}

type messageDocument struct {
	root     map[string]json.RawMessage
	messages []messageNode
	images   []imageRef
}
```

`parseMessages` 必须：

1. 把根对象解析为 `map[string]json.RawMessage`；
2. 要求 `messages` 存在且是数组；
3. 把每个 message 解析为字段 map；
4. 对字符串 content 只校验字符串合法；
5. 对数组 content 保存每个原始 block；
6. 只收集顶层 `type: image`；
7. 按 source 类型校验字段并生成不含日志输出的 `cachePayload`。

- [ ] **Step 4: 实现原位置替换和响应解析**

`rewrite` 用 `(messageIndex, blockIndex)` 建立替换表，将图片块替换为：

```go
replacement, err := json.Marshal(map[string]string{
	"type": "text",
	"text": descriptionPrefix + descriptions[imageIndex],
})
```

重新序列化发生过图片替换的 message 和根对象；未被替换的字段继续使用原 `json.RawMessage`。

`parseDescription` 解析：

```go
var response struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
}
```

对 `Type == "text"` 且 `strings.TrimSpace(Text) != ""` 的内容按顺序用 `"\n"` 连接；没有文本时返回 `vision response contains no text`。

`imageCacheKey` 对 provider、model、prompt、sourceType 和 `cachePayload` 加长度分隔后计算 SHA-256，返回十六进制字符串，避免把图片内容作为 map key 明文保存。

- [ ] **Step 5: 运行消息测试**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  sh -lc 'gofmt -w internal/vision/message.go internal/vision/message_test.go && go test ./internal/vision -run "Parse|Rewrite|Description|CacheKey" -count=1'
```

Expected: `ok anthropic-proxy/internal/vision`。

- [ ] **Step 6: 检查任务 diff，不提交**

Run:

```sh
git diff --check
git diff -- internal/vision/message.go internal/vision/message_test.go
```

---

### Task 3: TTL/LRU 缓存与并发请求合并

**Files:**

- Create: `internal/vision/cache.go`
- Create: `internal/vision/cache_test.go`

**Interfaces:**

- Produces:
  - `type resultSource uint8`
  - `const sourceCache, sourceLoaded, sourceShared`
  - `func newResultCache(capacity int, ttl time.Duration) *resultCache`
  - `func (c *resultCache) getOrLoad(ctx context.Context, key string, load func(context.Context) (string, error)) (string, resultSource, error)`
- Consumes: Task 2 生成的字符串缓存键

- [ ] **Step 1: 写出 TTL、LRU 和同键合并测试**

核心测试结构：

```go
func TestResultCacheCoalescesConcurrentLoads(t *testing.T) {
	cache := newResultCache(8, time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32

	load := func(context.Context) (string, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return "description", nil
	}

	type result struct {
		value  string
		source resultSource
		err    error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			value, source, err := cache.getOrLoad(context.Background(), "same", load)
			results <- result{value, source, err}
		}()
	}
	<-started
	close(release)

	first, second := <-results, <-results
	if first.err != nil || second.err != nil {
		t.Fatalf("errors: %v, %v", first.err, second.err)
	}
	if calls.Load() != 1 || first.value != "description" || second.value != "description" {
		t.Fatalf("calls=%d results=%+v %+v", calls.Load(), first, second)
	}
}
```

再增加：

- 注入可控 `cache.now`，验证 TTL 到期后重新 load；
- 容量 2 时访问顺序 `a,b,a,c` 淘汰 `b`；
- load 失败不写缓存；
- 一个共享等待方取消、另一个仍能拿到结果；
- 所有等待方取消时 loader 的 context 被取消。

- [ ] **Step 2: 验证缓存测试先失败**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  go test ./internal/vision -run ResultCache -count=1
```

Expected: 编译失败，缓存类型尚未定义。

- [ ] **Step 3: 实现 TTL/LRU 存储**

使用以下结构：

```go
type resultSource uint8

const (
	sourceCache resultSource = iota
	sourceLoaded
	sourceShared
)

type cacheEntry struct {
	key       string
	value     string
	expiresAt time.Time
}

type inflightCall struct {
	ctx     context.Context
	cancel  context.CancelFunc
	done    chan struct{}
	waiters int
	value   string
	err     error
}

type resultCache struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time
	lru      *list.List
	entries  map[string]*list.Element
	inflight map[string]*inflightCall
}
```

缓存命中时检查 `expiresAt` 并把元素移动到链表头。写入时更新已有元素；新元素放链表头；超过容量时删除链表尾。

- [ ] **Step 4: 实现共享 loader 生命周期**

`getOrLoad` 在锁内完成三路判断：

1. 有效缓存：返回 `sourceCache`；
2. 已有 inflight：`waiters++`，返回等待路径 `sourceShared`；
3. 新调用：用 `context.WithCancel(context.Background())` 创建 inflight，`waiters=1`，启动一个 loader goroutine并返回等待路径 `sourceLoaded`。

loader 完成时在锁内：

```go
if err == nil && value != "" {
	c.putLocked(key, value)
}
call.value = value
call.err = err
delete(c.inflight, key)
close(call.done)
call.cancel()
```

等待方在 `call.done` 与自己的 `ctx.Done()` 之间选择。取消时递减 `waiters`；只有减为零才调用共享 `call.cancel()`。这样单个客户端断开不会中止仍被其他请求需要的识图调用。

- [ ] **Step 5: 运行缓存测试和 race 检查**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  sh -lc 'gofmt -w internal/vision/cache.go internal/vision/cache_test.go && go test -race ./internal/vision -run ResultCache -count=1'
```

Expected: 测试通过且无 data race。

- [ ] **Step 6: 检查任务 diff，不提交**

Run:

```sh
git diff --check
git diff -- internal/vision/cache.go internal/vision/cache_test.go
```

---

### Task 4: 影子模型 HTTP 客户端

**Files:**

- Create: `internal/vision/client.go`
- Create: `internal/vision/client_test.go`

**Interfaces:**

- Consumes:
  - `config.VisionConfig`
  - `provider.Rule`
  - Task 2 的 `imageRef`、`parseDescription`
  - `stats.DB.RecordAsync`
- Produces:
  - `type describer interface { Describe(context.Context, http.Header, imageRef) (string, error) }`
  - `type visionClient struct`
  - `func newVisionClient(cfg *config.Config, httpClient *http.Client, sdb *stats.DB) *visionClient`
  - `func effectivePrompt(configured string) string`

- [ ] **Step 1: 写出请求结构、允许头和响应解析测试**

使用 `httptest.Server` 捕获请求，构造 `config.Config`，调用 `Describe`。断言：

```go
if request.Model != "sonnet" || request.MaxTokens != 2048 || request.Stream {
	t.Fatalf("wrong shadow request: %+v", request)
}
if len(request.Messages) != 1 ||
	request.Messages[0].Role != "user" ||
	len(request.Messages[0].Content) != 2 {
	t.Fatalf("wrong messages: %+v", request.Messages)
}
```

原请求头同时放入允许项和敏感的无关项：

```go
headers := http.Header{
	"Authorization":     {"Bearer secret"},
	"X-Api-Key":         {"key"},
	"Anthropic-Version": {"2023-06-01"},
	"Anthropic-Beta":    {"files-api-2025-04-14"},
	"Cookie":            {"must-not-copy"},
	"Accept-Encoding":   {"gzip"},
}
```

断言前四项被复制，后两项为空，`Content-Type` 为 `application/json`。测试响应返回两个 text block，期望 `"first\nsecond"`。

- [ ] **Step 2: 写出重试、超时和 usage 测试**

增加三个测试：

1. 上游先返回匹配 overload rule 的错误，再成功；把 `client.sleep` 替换为记录 duration 的无等待函数，断言调用两次；
2. 上游返回不匹配规则的 500，断言只调用一次并返回错误；
3. 设置短 timeout，上游等待 context 取消，断言 `errors.Is(err, context.DeadlineExceeded)`。

为 usage 测试把 `client.recordUsage` 替换为 channel 回调；成功响应含：

```json
{
  "model":"sonnet",
  "content":[{"type":"text","text":"description"}],
  "usage":{
    "input_tokens":10,
    "output_tokens":20,
    "cache_read_input_tokens":3
  }
}
```

断言回调收到完整响应字节；错误响应和缓存不经过此客户端记录。

- [ ] **Step 3: 验证客户端测试先失败**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  go test ./internal/vision -run VisionClient -count=1
```

Expected: 编译失败，`visionClient` 尚未定义。

- [ ] **Step 4: 实现子请求和允许头复制**

定义固定内置提示词 `defaultPrompt`。`effectivePrompt` 在配置值经 `strings.TrimSpace` 后非空时原样返回配置值，否则返回 `defaultPrompt`。再定义：

```go
type describer interface {
	Describe(context.Context, http.Header, imageRef) (string, error)
}

type visionClient struct {
	providerName string
	upstream     string
	cfg          config.VisionConfig
	rules        []provider.Rule
	httpClient   *http.Client
	sleep        func(context.Context, time.Duration) error
	recordUsage  func([]byte)
}
```

`newVisionClient`：

- 保存 `strings.TrimRight(cfg.Upstream, "/") + "/v1/messages"` 所需字段；
- 复制 overload rules，避免外部修改；
- 设置可取消的 sleep；
- 当 `sdb != nil` 时，闭包调用：

```go
sdb.RecordAsync(cfg.ProviderName, "/v1/messages#vision", body, stats.NewParser("anthropic"))
```

请求 body 只包含 `model`、`max_tokens`、`stream:false` 和单个 user message；图片块使用 `image.block` 原始 JSON。只复制四个允许头，重新设置 JSON Content-Type。

- [ ] **Step 5: 实现有界重试、响应读取和描述解析**

`Describe` 先用 `context.WithTimeout(ctx, c.cfg.Timeout)`。每次请求都重新创建 `http.Request` 和 body reader。

规则：

- 网络错误使用第一条 overload rule 的次数与退避；
- 非 2xx 响应先读取并关闭 body；
- `provider.Match` 命中时用首次命中的 rule 重试；
- 未命中立即返回包含 status code、但不包含响应正文的错误；
- 总调用数为 `1 + rule.MaxRetries`；
- 等待使用 `c.sleep`，默认实现 select `ctx.Done()` 和 `time.NewTimer`；
- 2xx 响应解析成功后先调用 `recordUsage`，再返回文本；
- 读取错误、非法 JSON 或空文本返回错误。

- [ ] **Step 6: 运行客户端测试**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  sh -lc 'gofmt -w internal/vision/client.go internal/vision/client_test.go && go test ./internal/vision -run VisionClient -count=1'
```

Expected: 客户端测试全部通过。

- [ ] **Step 7: 检查任务 diff，不提交**

Run:

```sh
git diff --check
git diff -- internal/vision/client.go internal/vision/client_test.go
```

---

### Task 5: 多图预处理编排

**Files:**

- Create: `internal/vision/preprocessor.go`
- Create: `internal/vision/preprocessor_test.go`

**Interfaces:**

- Consumes:
  - Task 2 的 `parseMessages`、`rewrite`、`imageCacheKey`
  - Task 3 的 `resultCache.getOrLoad`
  - Task 4 的 `describer`
- Produces:
  - `type Preprocessor struct`
  - `func New(cfg *config.Config, httpClient *http.Client, sdb *stats.DB) *Preprocessor`
  - `func newPreprocessor(provider string, cfg config.VisionConfig, d describer, cache *resultCache) *Preprocessor`
  - `func (p *Preprocessor) Process(ctx context.Context, headers http.Header, body []byte) ([]byte, error)`
  - `func HTTPStatus(err error) int`

- [ ] **Step 1: 写出无图直通、多图并发和顺序测试**

定义 fake：

```go
type fakeDescriber struct {
	mu        sync.Mutex
	calls     int
	active    int
	maxActive int
	describe  func(imageRef) (string, error)
}

func (f *fakeDescriber) Describe(ctx context.Context, _ http.Header, image imageRef) (string, error) {
	f.mu.Lock()
	f.calls++
	f.active++
	if f.active > f.maxActive {
		f.maxActive = f.active
	}
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.active--
		f.mu.Unlock()
	}()
	return f.describe(image)
}
```

测试：

- 无图请求返回与输入完全相同的 `[]byte`，fake 调用为零；
- 三张图片让 fake 返回不同描述，即使完成顺序不同，改写顺序仍与图片一致；
- `max_concurrency: 2` 时，五张图片的 `maxActive` 不超过 2；
- 两个相同图片块只执行一次 fake 调用；
- 任一图片返回错误时 `Process` 返回 502 分类错误且不返回改写 body。

- [ ] **Step 2: 写出 400 分类和取消测试**

非法 JSON、非法 source 分别断言：

```go
if status := HTTPStatus(err); status != http.StatusBadRequest {
	t.Fatalf("status=%d err=%v", status, err)
}
```

识图失败断言 502。取消请求 context 后断言所有等待任务退出，不再取得 semaphore，也不会产生额外 fake 调用。

- [ ] **Step 3: 验证预处理器测试先失败**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  go test ./internal/vision -run Preprocessor -count=1
```

Expected: 编译失败，`Preprocessor` 尚未定义。

- [ ] **Step 4: 实现错误分类和构造函数**

定义：

```go
type processError struct {
	status int
	err    error
}

func (e *processError) Error() string { return e.err.Error() }
func (e *processError) Unwrap() error { return e.err }

func HTTPStatus(err error) int {
	var target *processError
	if errors.As(err, &target) {
		return target.status
	}
	return http.StatusBadGateway
}

type Preprocessor struct {
	provider  string
	model     string
	prompt    string
	describer describer
	cache     *resultCache
	slots     chan struct{}
}
```

`New` 用 `newVisionClient` 和 `newResultCache` 组装，并调用：

```go
func newPreprocessor(provider string, cfg config.VisionConfig, d describer, cache *resultCache) *Preprocessor {
	return &Preprocessor{
		provider:  provider,
		model:     cfg.Model,
		prompt:    effectivePrompt(cfg.Prompt),
		describer: d,
		cache:     cache,
		slots:     make(chan struct{}, cfg.MaxConcurrency),
	}
}
```

测试通过该构造函数注入 fake describer 和独立 cache。

- [ ] **Step 5: 实现全有或全无并发流程**

`Process`：

1. 调用 `parseMessages`，解析错误包装为 400；
2. 图片列表为空时原样返回 `body`；
3. 为每张图启动 goroutine，输出写入固定下标的 `descriptions`；
4. 每个 goroutine 调用 `cache.getOrLoad`；
5. loader 先等待 `slots <- struct{}{}` 或 context 取消，结束时释放 slot；
6. loader 调用 `describer.Describe`；
7. 首个错误触发本请求的派生 context 取消，但等待所有 goroutine退出；
8. 任一错误包装为 502；原请求 context 已取消时保留 `context.Canceled`/`DeadlineExceeded`；
9. 全部成功后调用 `doc.rewrite(descriptions)`。

日志只包含 provider、图片序号、source type、cache source、耗时和错误类型；不输出 URL、file ID、base64、完整请求或鉴权头。

- [ ] **Step 6: 运行预处理器测试和 vision 包 race 检查**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  sh -lc 'gofmt -w internal/vision/preprocessor.go internal/vision/preprocessor_test.go && go test -race ./internal/vision -count=1'
```

Expected: 全部 vision 测试通过且无 race。

- [ ] **Step 7: 检查任务 diff，不提交**

Run:

```sh
git diff --check
git diff -- internal/vision/preprocessor.go internal/vision/preprocessor_test.go
```

---

### Task 6: 接入 Proxy、文档与端到端验证

**Files:**

- Modify: `internal/proxy/proxy.go:4-49`
- Create: `internal/proxy/proxy_test.go`
- Modify: `config.yaml`
- Modify: `README.md`

**Interfaces:**

- Consumes:
  - `config.Config.Vision`
  - `vision.New(...)`
  - `vision.Preprocessor.Process(...)`
  - `vision.HTTPStatus(...)`
- Produces: 保持 `proxy.New(cfg, client, sdb) http.Handler` 公共签名不变

- [ ] **Step 1: 写出 Handler 集成测试**

在 `proxy_test.go` 建立一个 `httptest.Server` 上游。按请求 body 的 `model` 区分影子请求和主请求：

```go
switch request.Model {
case "sonnet":
	atomic.AddInt32(&visionCalls, 1)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{
	  "model":"sonnet",
	  "content":[{"type":"text","text":"screen description"}],
	  "usage":{"input_tokens":10,"output_tokens":20}
	}`)
case "main-model":
	atomic.AddInt32(&mainCalls, 1)
	mainBody <- append([]byte(nil), body...)
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{
	  "model":"main-model",
	  "content":[{"type":"text","text":"done"}],
	  "usage":{"input_tokens":30,"output_tokens":40}
	}`)
}
```

覆盖：

- 启用且有图：一次影子调用、一次主调用，主 body 没有 image 且包含描述；
- 启用但无图：零次影子调用、一次主调用，body 字节相同；
- 影子失败：代理返回 502，主调用为零；
- vision 禁用：图片 body 原样进入主调用；
- 非 `/v1/messages`、非 POST 或非 JSON Content-Type：不预处理；
- 原请求取消：主调用为零。

- [ ] **Step 2: 验证集成测试先失败**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  go test ./internal/proxy -run Vision -count=1
```

Expected: 有图测试失败，因为 handler 尚未调用预处理器。

- [ ] **Step 3: 在 handler 中接入预处理器**

给 `handler` 增加：

```go
vision *vision.Preprocessor
```

`New` 在 `cfg.Vision.Enabled` 时调用 `vision.New(cfg, client, sdb)`。为避免字段名与包名冲突，局部变量使用 `visionPreprocessor`。

增加资格判断：

```go
func shouldPreprocessVision(r *http.Request) bool {
	if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	return err == nil && mediaType == "application/json"
}
```

在读取并关闭原 body 后、进入现有重试循环前：

```go
if h.vision != nil && shouldPreprocessVision(r) {
	body, err = h.vision.Process(r.Context(), r.Header, body)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		status := vision.HTTPStatus(err)
		http.Error(w, http.StatusText(status), status)
		return
	}
}
```

现有 target、重试循环、stream、forward 和统计代码不改。

- [ ] **Step 4: 更新示例配置而不覆盖本地规则**

在 `config.yaml` 的 `jdcloud` provider 中加入默认关闭示例：

```yaml
    vision:
      enabled: false
      model: sonnet
      max_tokens: 2048
      timeout: 2m
      max_concurrency: 4
      cache_ttl: 30m
      cache_max_entries: 512
      # prompt: 留空时使用内置详细描述提示词
```

保留现有三条 overload rules，包括 `body_contains: "未找到"`。

- [ ] **Step 5: 更新 README**

新增“图片预处理”章节，明确：

- 仅 Anthropic `POST /v1/messages`；
- 三种 source；
- 默认关闭、默认模型 `sonnet`；
- 同一 upstream 与原鉴权头；
- 图片被文本替换，任一失败返回 502；
- TTL/LRU 缓存和并发默认值；
- Files API 请求必须携带对应 `Anthropic-Beta` 头；
- `tool_result.content` 和 OpenAI 协议不在首版范围；
- 影子请求 usage 以 `/v1/messages#vision` 计入统计。

在“config.yaml 完整结构”中加入完整 `vision` 字段说明。

- [ ] **Step 6: 运行全部测试、race 和构建**

Run:

```sh
docker run --rm \
  -v /Users/Euphie/Documents/Go/Euphie/anthropic-proxy:/src \
  -w /src golang:1.25 \
  sh -lc 'gofmt -w internal/config/*.go internal/vision/*.go internal/proxy/*.go && go test ./... && go test -race ./... && go build -o /tmp/anthropic-proxy ./cmd/anthropic-proxy'
```

Expected:

- 所有包测试通过；
- race 检查无报告；
- `go build` 成功；
- 不在宿主机生成 `bin/` 或其他构建产物。

- [ ] **Step 7: 最终规格覆盖检查**

Run:

```sh
git diff --check
git status --short
git diff --stat
```

逐项核对设计文档的配置、三类图片源、并发、缓存、错误、统计、日志、禁用兼容和验收标准。保留 `config.yaml` 的本地规则，不执行任何 Git 提交或推送。
