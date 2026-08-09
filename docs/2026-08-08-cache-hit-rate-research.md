# 明细列表“缓存命中率”指标调研

日期：2026-08-08
范围：只评估逐请求明细（`/requests`）增加缓存命中率/缓存读取占比；未修改代码。基线提交：`6ad5796da1b984a6f82e7ccf0b4da89158f796d6`。

## 结论

不应把所有 provider 的命中率固定为 `cache_read / input_tokens`。这个比值只在 `input_tokens` 已经包含缓存部分时成立（OpenAI、Gemini 等）；Anthropic 的 `input_tokens` 明确**不含**缓存 read/write，正确分母必须加入 `cache_creation` 和 `cache_read`。

建议在明细列表把指标命名为“缓存读取占输入”（或在“缓存命中率”旁明确说明是 token 加权比例），而不是把它与现有二元 `cache_hit` 混为一谈：一次请求可只缓存其前缀，因此该比例不一定是 0% 或 100%。现有 `cache_hit` 更接近“本请求是否发生过 cache read”。

推荐的计算目标是：

```text
cache_read_ratio = cache_read_tokens / effective_input_tokens
```

其中 `cache_read_tokens` 只能取一个来源：优先 `CacheReadTokens`；其为 0 时才回退 `CachedTokens`，不得相加。仓库已有同一回退规则用于成本与趋势图（[/Users/patrickfu/dev/cap-token-usage-tracker/cost.go:414-418](/Users/patrickfu/dev/cap-token-usage-tracker/cost.go:414)、[/Users/patrickfu/dev/cap-token-usage-tracker/dashboard.go:358](/Users/patrickfu/dev/cap-token-usage-tracker/dashboard.go:358)）。

| 已能确认的 provider / API 语义 | `effective_input_tokens`（分母） | 每请求公式 | 依据 |
| --- | --- | --- | --- |
| OpenAI Chat Completions / Responses / Usage API | `InputTokens` | `cacheRead / InputTokens` | OpenAI 将 `cached_tokens` 定义为从缓存取回的输入 token；组织 Usage API 进一步明确 `input_tokens` 包含 cached 与 cache-write token。见 [Chat Completions usage object](https://platform.openai.com/docs/api-reference/chat/object) 与 [Organization Usage API](https://developers.openai.com/api/reference/resources/admin/subresources/organization/subresources/usage)。 |
| Anthropic Messages / Claude | `InputTokens + CacheCreationTokens + cacheRead` | `cacheRead / (InputTokens + CacheCreationTokens + cacheRead)` | Anthropic 明确：`input_tokens` 是最后 breakpoint 后、未 read/未 write cache 的 token，并给出总输入相加式。见 [Prompt caching: Tracking cache performance](https://platform.claude.com/docs/en/build-with-claude/prompt-caching#tracking-cache-performance)。 |
| Gemini GenerateContent | `InputTokens`（应映射自 `promptTokenCount`） | `cacheRead / InputTokens` | Gemini 明确 `promptTokenCount` 是完整 effective prompt，设定 `cachedContent` 后仍含 cached content；`cachedContentTokenCount` 是其中缓存部分。见 [GenerateContent `UsageMetadata`](https://ai.google.dev/api/generate-content#UsageMetadata)。 |
| 未知 provider / OpenAI-compatible 转发层 | **未知，除非映射层证明 `InputTokens` 是完整 prompt** | 不显示比例 | 本仓库只保存无类型计数，未含 upstream usage 映射；不能凭“非 Anthropic”或模型名推断。 |

`CacheCreationTokens` 是 cache write，不是命中。它仅在 Anthropic 这类“Input 不含 cache”的口径中加入分母；在 OpenAI/Gemini 的完整 prompt 口径中再加一次会双计。Gemini 的显式缓存创建是独立 cache-service 操作，不能作为其 `GenerateContent` 调用的分母组成部分；其官方 caching 文档也说明 cached content 是 prompt 的前缀，且缓存 token 会在使用 cache 的 `GenerateContent` usage 中返回。见 [Gemini context caching](https://ai.google.dev/gemini-api/docs/generate-content/caching)。

## 官方字段语义与公式推导

### OpenAI

OpenAI Chat Completions 把 `prompt_tokens` 定义为 prompt token 数，`prompt_tokens_details.cached_tokens` 是该 prompt 中的 cached token；组织 Usage API 明确 `input_tokens` “including cached and cache-write tokens”，而 `input_cached_tokens` 是来自先前请求的 cached 文本输入。故 cached 是完整输入的子集，安全公式为：

```text
cache_read_ratio_openai = cached_tokens / input_tokens
```

不要使用 `cached / (input + cached)`，因为缓存 token 已在 `input` 内；也不要把 OpenAI 的 cache write 再加入分母。

### Anthropic / Claude

Anthropic 的三个字段是互斥输入分区：

- `cache_read_input_tokens`：本请求从 cache 取回的 token；
- `cache_creation_input_tokens`：本请求写入新 cache entry 的 token；
- `input_tokens`：既未 read、也未用于创建 cache 的最后 breakpoint 后 token。

官方给出的总输入公式是：

```text
total_input_tokens = cache_read_input_tokens
                   + cache_creation_input_tokens
                   + input_tokens
```

因此：

```text
cache_read_ratio_anthropic = cache_read_input_tokens / total_input_tokens
```

示例：`read=80, creation=10, input=10` 时应为 `80%`。若沿用当前 `80 / 10`，结果为 800%，现有趋势 helper 再截断到 100%，会错误地显示为全命中。

### Gemini

Gemini `UsageMetadata.promptTokenCount` 是总 effective prompt size，显式 `cachedContent` 时仍包含 cached content；`cachedContentTokenCount` 是其中缓存部分。因此：

```text
cache_read_ratio_gemini = cachedContentTokenCount / promptTokenCount
```

这与 OpenAI 口径相同。只要上游确实把两字段映射到本仓库的 `InputTokens` 与 `CacheReadTokens`/`CachedTokens`，不应把 cache creation 加入 Gemini 生成请求的分母。

## 当前仓库核对

1. 数据模型保留 `InputTokens`、`CachedTokens`、`CacheReadTokens`、`CacheCreationTokens`，但没有字段存在性或原始 provider usage schema 标记。见 [/Users/patrickfu/dev/cap-token-usage-tracker/aggregate.go:26-55](/Users/patrickfu/dev/cap-token-usage-tracker/aggregate.go:26)。
2. 解码器通过 `firstInt64` 读取四个计数，再用 `positiveUint` 写入 `uint64`。缺字段、原始值为 0、负数都会在存储后表现为 0，因此丢失“明确的 0%”与“上游未提供字段”的差异。见 [/Users/patrickfu/dev/cap-token-usage-tracker/usage.go:22-90](/Users/patrickfu/dev/cap-token-usage-tracker/usage.go:22)。
3. 明细 API 已返回全部计数和 `cache_hit`；前端已有 `Cache read`、`Cache creation` 与二元 `Cache hit` 列。见 [/Users/patrickfu/dev/cap-token-usage-tracker/request_log.go:15-67](/Users/patrickfu/dev/cap-token-usage-tracker/request_log.go:15)、[/Users/patrickfu/dev/cap-token-usage-tracker/dashboard.go:307-345](/Users/patrickfu/dev/cap-token-usage-tracker/dashboard.go:307)。
4. `cache_hit` 仅以 `CacheReadTokens > 0` 判定，并未使用 `CachedTokens` 兼容回退；成本和趋势图则会回退。因此只有旧/兼容 `CachedTokens` 的请求可能在明细中显示“miss”，但在趋势/成本逻辑中作为 cache read 使用。见 [/Users/patrickfu/dev/cap-token-usage-tracker/request_log.go:66](/Users/patrickfu/dev/cap-token-usage-tracker/request_log.go:66)、[/Users/patrickfu/dev/cap-token-usage-tracker/cost.go:414-418](/Users/patrickfu/dev/cap-token-usage-tracker/cost.go:414)、[/Users/patrickfu/dev/cap-token-usage-tracker/dashboard.go:358](/Users/patrickfu/dev/cap-token-usage-tracker/dashboard.go:358)。
5. 当前趋势图无条件计算 `cacheRead / input`，并夹在 `[0,100]`；测试锁定该实现且显式拒绝把 cache read 加进分母。它对 OpenAI/Gemini 语义正确，对 Anthropic 错误。见 [/Users/patrickfu/dev/cap-token-usage-tracker/dashboard.go:358-360](/Users/patrickfu/dev/cap-token-usage-tracker/dashboard.go:358)、[/Users/patrickfu/dev/cap-token-usage-tracker/dashboard_test.go:330-346](/Users/patrickfu/dev/cap-token-usage-tracker/dashboard_test.go:330)。
6. 成本模块已经区分两类 accounting mode：`provider == "anthropic"` 或 `executor == "claude"` 时 Input excludes cache，其他**及未知** provider 默认 Input includes cache。README 也描述同一规则。见 [/Users/patrickfu/dev/cap-token-usage-tracker/cost.go:414-479](/Users/patrickfu/dev/cap-token-usage-tracker/cost.go:414)、[/Users/patrickfu/dev/cap-token-usage-tracker/README.md:140](/Users/patrickfu/dev/cap-token-usage-tracker/README.md:140)、[/Users/patrickfu/dev/cap-token-usage-tracker/README.md:424](/Users/patrickfu/dev/cap-token-usage-tracker/README.md:424)。

第 6 点足以支持 Anthropic 的 per-provider 公式，但不应把“未知 provider 的成本默认值”升级为“未知 provider 的遥测语义事实”：成本默认值用于避免重复计价，而命中率显示会把不完整或错口径数据直接呈现给用户。

## 明细列表的推荐展示规则

### 计算规则

在上游计数字段均有可信语义时，按下面逻辑计算；使用 token 数之比，并保留两位小数：

```text
read = CacheReadTokens > 0 ? CacheReadTokens : CachedTokens

if provider == "anthropic" || executor == "claude":
    denominator = InputTokens + CacheCreationTokens + read
else if provider in verified_input_includes_cache_providers:
    denominator = InputTokens
else:
    ratio = unavailable

ratio = denominator > 0 ? read / denominator : unavailable
```

`verified_input_includes_cache_providers` 初始应只含已经验证上游映射的 provider，而不是当前“除 Anthropic 外全部”的成本默认分支。若 CLIProxyAPI 实际使用的 provider 名称为 `google` 而不是 `gemini`，应以该上游 provider/executor 的真实枚举为准再加入；本仓库未包含该映射实现，不能凭模型名猜测。

### 缺失、零值与历史数据

- **分母为 0：**显示 `—`/“不可计算”，不要显示 `0%`；`0 / 0` 没有命中率含义。
- **`read > denominator`：**显示 `—` 并记录数据口径异常；不要像当前趋势一样截断到 100%，否则会掩盖 Anthropic 或上游映射错误。
- **read 为 0：**只有确认上游提供了 cache-read 字段且该字段确为 0 时显示 `0.0%`。当前持久化模型没有 presence bit，因此旧记录、未知 provider、兼容转发商应显示 `—`，不能把“未提供”解释成“未命中”。
- **只含 `CachedTokens` 的兼容记录：**若能确认该字段是 cache read，则可用作 `read`；否则显示 `—`。不要与 `CacheReadTokens` 相加。
- **cache write：**Anthropic 首次预热通常 `read=0, creation>0`，应是有效的 `0.00%`（字段存在时），不是命中；Gemini 显式 cache creation 与生成调用分离，不能把跨调用 write 加到本行。
- **失败或未完整返回 usage 的请求：**不显示比例。HTTP 成功也不足以证明使用数据完整。
- **历史数据：**没有原始字段存在性、原始 API 类型和 schema version 的历史条目不能可靠补算为 0%；建议新字段/新记录显式保存 `cache_usage_known`（以及建议的 `input_accounting_mode`），旧数据一律 `—`，或明确标记为“兼容估算”。

## 实施前的验收标准

1. OpenAI/Gemini 完整输入样例：`input=100, read=60, creation=0` 显示 `60.00%`。
2. Anthropic 样例：`input=10, read=80, creation=10` 显示 `80.00%`，不得显示 `100%`。
3. Anthropic 首次写 cache：`input=10, read=0, creation=90` 在字段存在时显示 `0.00%`。
4. `input=0, read=0`、未知 provider、缺失 cache usage、`read > denominator` 显示 `—`，不显示 `0%` 或截断 `100%`。
5. `CachedTokens=50, CacheReadTokens=0` 的兼容映射只计一次；二元 `cache_hit`、新比例、成本和趋势图应使用同一 `read` 选择规则。
6. 如果明细列表使用 per-provider 公式，趋势图同名“缓存命中率”也必须改为同一 provider-aware 聚合：先分别累计每条记录的 `read` 与对应 `effective_input`，再计算 `sum(read) / sum(effective_input)`；不能把不同 provider 的 `InputTokens` 直接合并后统一相除。

## 未解决风险

1. 本仓库只接收通用 `UsageRecord` 并做数值归一化，未包含 CLIProxyAPI 如何把各厂商原始 usage 映射为 `CachedTokens`、`CacheReadTokens`、`CacheCreationTokens` 的源码。因而 OpenAI/Anthropic/Gemini 的官方字段语义已核实，但“当前所有上游记录都保真映射”的事实尚未在本仓库内证实。
2. `Provider`/`ExecutorType` 的真实枚举、OpenAI-compatible 转发商及各模型是否回传 cache usage 需要在上游代码或实测 payload 逐一确认；不得以模型名、成本价格来源或“非 Anthropic”推断。
3. 当前趋势图测试强制全局 `cacheRead / input`。若要保证同一标签跨明细和趋势一致，变更必须同步替换该测试与聚合策略；仅加一列而不修正趋势会留下两个互相矛盾的“缓存命中率”。

## 实施决策

用户决定保留现有“缓存读取”绝对 Token 列和“缓存命中”是/否列，另在逐请求明细中新增“缓存命中率”百分比列，保留两位小数；无法可靠计算时显示 `—`。本次不改变趋势图口径：`model_series` / `series` 不携带 `provider` 或 `executor_type`，在不改后端响应或聚合结构的约束下无法按 provider 正确加权。逐请求 `/requests` 数据含完整维度，因此新列仍可完全在前端安全计算。
