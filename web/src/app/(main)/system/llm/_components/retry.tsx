"use client";

// LLM 重试配置的共用件：五层重试各自的「次数 + 间隔」。
//
// 五层从内到外：建连(SDK) → 空响应(SDK) → 同 provider 安全窗口 → 轮询熔断 → 意图重跑。
// 前三层跟着端点走，所以每个模型配置都能覆盖全局默认；后两层是进程级的，只有全局一份。
//
// 所有输入都遵循同一套「留空 = 不配置」语义，与后端 db.RetryRule 一致：
//   次数   空/0 = 用内置默认 | -1 = 关闭这层重试 | >0 = 用这个次数
//   间隔   空/0 = 用这层原本的指数退避 | >0 = 改用这个固定毫秒间隔

import * as React from "react";

import { Loader2Icon, SaveIcon } from "lucide-react";
import { toast } from "sonner";

import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { api } from "@/lib/api";
import type { LLMRetryOverride, LLMRetryPolicy, LLMRetryRule } from "@/lib/types";

export const ZERO_RULE: LLMRetryRule = { attempts: 0, interval_ms: 0 };
export const ZERO_OVERRIDE: LLMRetryOverride = {
  connect: ZERO_RULE,
  empty: ZERO_RULE,
  stream: ZERO_RULE,
};
const ZERO_POLICY: LLMRetryPolicy = {
  ...ZERO_OVERRIDE,
  breaker: ZERO_RULE,
  intent: ZERO_RULE,
};

type LayerMeta = {
  title: string;
  /** 这层重试发生在哪、由谁执行 */
  where: string;
  /** 什么样的错误会走到这层——具体到状态码，别让人猜 */
  trigger: string;
  /** 长得像但【不】走这层的错误，省得填了没反应还以为是 bug */
  skips?: string;
  desc: string;
  attemptsLabel: string;
  /** 次数留空时的默认值，用于占位符 */
  defAttempts: number;
  /** 间隔留空时的默认策略，用于占位符 */
  defInterval: string;
  /** 次数填 -1 的含义 */
  offHint: string;
};

export const RETRY_LAYERS = {
  connect: {
    title: "Connection Retries",
    where: "SDK · Before receiving HTTP 200",
    trigger:
      "Connection fails or HTTP 200 has not been received: network errors such as connection resets, read/write timeouts, and DNS failures, plus HTTP 408, 429, 500, 502, 503, and 504.",
    skips:
      "Other status codes (400 / 401 / 403 / 404 / 413 / 422, etc.) are deterministic rejections. Retrying will fail again, so the error is propagated immediately.",
    desc: "Resends the same request unchanged. Once streaming starts (HTTP 200 received), interruptions are no longer handled by this layer.",
    attemptsLabel: "Retry Count",
    defAttempts: 3,
    defInterval: "0.5s→1s→2s exponential (8s cap)",
    offHint: "-1 = no retries; propagate failures immediately",
  },
  empty: {
    title: "Empty Response Retries",
    where: "SDK · OpenAI format only",
    trigger:
      "HTTP 200 with a normal stop finish_reason, but no content blocks in the entire response. Empty gateway frames, dropped reasoning fields, and sampling glitches can cause this.",
    skips:
      "Empty output caused by max_tokens truncation is excluded (increase the output limit instead; resending would hit the same limit).",
    desc: "Resends the entire prompt, which is expensive for long contexts. Keep the retry count low.",
    attemptsLabel: "Retry Count",
    defAttempts: 2,
    defInterval: "0.5s→1s→2s exponential (8s cap)",
    offHint: "-1 = return empty responses unchanged",
  },
  stream: {
    title: "Same-provider Safe-window Retries",
    where: "Application · Before delivering output",
    trigger:
      "An error occurs after the stream is established (HTTP 200 received): connection interruption, provider overloaded, or an in-stream 429 / 5xx error event, before any tokens have been delivered to the caller.",
    skips:
      "No retries for exhausted quota (402 / insufficient_quota, handled by rotating configurations), excessive context length (413 / context length, handled by compression), or deterministic rejections (400 / 401 / 403 / 404 / 422).",
    desc: "Replays the same request using the same configuration. Since no output has been delivered, replay cannot duplicate model output or tool execution.",
    attemptsLabel: "Retry Count",
    defAttempts: 2,
    defInterval: "0.5s→1s exponential (4s cap)",
    offHint: "-1 = pass stream interruptions directly to the outer intent rerun layer",
  },
  breaker: {
    title: "Rotation Circuit Breaker",
    where: "Application · Process-wide, shared globally",
    trigger:
      "Trips after consecutive transient failures (429, 5xx, network errors) reach the threshold. Deterministic failures such as insufficient balance (402), invalid keys (401 / 403), or missing models (404) trip it immediately, regardless of the threshold.",
    skips: "One success resets the counter, so occasional failures do not accumulate indefinitely.",
    desc: "After tripping, the configuration enters cooldown and is skipped during rotation. State is persisted and survives restarts.",
    attemptsLabel: "Consecutive Failures Before Tripping",
    defAttempts: 3,
    defInterval: "1min→5min→30min stepped",
    offHint: "-1 = never trip for transient failures (deterministic failures still trip)",
  },
  intent: {
    title: "Intent Reruns",
    where: "Application · Process-wide, shared globally",
    trigger:
      "Earlier layers could not recover: the worker ends with model_error because all inner retries are exhausted, or the stream breaks after output delivery begins (replay is unsafe, so the entire intent must restart).",
    skips:
      "Exhausted quota is handled by configuration rotation, not reruns here. Pausing, terminating, or wrapping up a task takes precedence immediately without waiting for backoff.",
    desc: "Reruns the entire intent from the beginning. This is the outermost layer, so each rerun multiplies the attempts of the inner layers.",
    attemptsLabel: "Rerun Count",
    defAttempts: 2,
    defInterval: "Fixed 3s",
    offHint: "-1 = no reruns; mark the intent as blocked immediately",
  },
} satisfies Record<string, LayerMeta>;

type LayerKey = keyof typeof RETRY_LAYERS;

/** 毫秒的人话，只用于在输入框旁边回显，免得数零。 */
function humanMs(ms: number) {
  if (!Number.isFinite(ms) || ms <= 0) return "";
  if (ms < 1000) return `${ms}ms`;
  if (ms < 60_000) return `${Number((ms / 1000).toFixed(2))}s`;
  return `${Number((ms / 60_000).toFixed(2))}min`;
}

/** 受控数字输入：空串 ↔ 0，中间态（"-"、"1e"）原样留在本地，不打扰父级。 */
function NumField({
  id,
  value,
  onChange,
  placeholder,
  min,
}: {
  id: string;
  value: number;
  onChange: (n: number) => void;
  placeholder: string;
  min: number;
}) {
  const [text, setText] = React.useState(value === 0 ? "" : String(value));
  // 父级换了一整套值（读取到策略、切换配置）时跟上；自己敲字时不会走到这里，
  // 因为那时 value 已经等于本地文本 parse 后的结果。
  React.useEffect(() => {
    const incoming = value === 0 ? "" : String(value);
    setText((cur) => (Number(cur || 0) === value ? cur : incoming));
  }, [value]);
  return (
    <Input
      id={id}
      type="number"
      min={min}
      className="w-28 shrink-0"
      value={text}
      placeholder={placeholder}
      onChange={(e) => {
        setText(e.target.value);
        const n = Number(e.target.value);
        onChange(e.target.value.trim() === "" || !Number.isFinite(n) ? 0 : Math.trunc(n));
      }}
    />
  );
}

/** 一层重试的两个旋钮。idPrefix 用来在同一页出现多次时保住 label 的 htmlFor。 */
export function RetryRuleFields({
  layer,
  idPrefix,
  value,
  onChange,
  compact,
}: {
  layer: LayerKey;
  idPrefix: string;
  value: LLMRetryRule;
  onChange: (r: LLMRetryRule) => void;
  /** true = 配置抽屉里的紧凑版：省掉展开说明，只留「什么错误会走到这层」这一句 */
  compact?: boolean;
}) {
  const meta = RETRY_LAYERS[layer];
  const human = humanMs(value.interval_ms);
  return (
    <div className={compact ? "grid gap-2" : "grid gap-3 rounded-lg border p-3"}>
      <div className="grid gap-0.5">
        <div className="flex flex-wrap items-baseline gap-2">
          <Label className="text-sm">{meta.title}</Label>
          <span className="text-muted-foreground text-xs">{meta.where}</span>
        </div>
        {/* 哪些错误会走到这层，具体到状态码——填了旋钮却看不到效果，多半是错误压根不落在这层。 */}
        <p className="text-muted-foreground text-xs">
          <span className="font-medium text-foreground">Trigger</span>: {meta.trigger}
        </p>
        {!compact && meta.skips && (
          <p className="text-muted-foreground text-xs">
            <span className="font-medium text-foreground">Excluded</span>: {meta.skips}
          </p>
        )}
        {!compact && <p className="text-muted-foreground text-xs">{meta.desc}</p>}
      </div>
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-n`} className="text-muted-foreground text-xs">
            {meta.attemptsLabel}
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-n`}
            min={-1}
            value={value.attempts}
            placeholder={`Default ${meta.defAttempts}`}
            onChange={(n) => onChange({ ...value, attempts: n })}
          />
        </div>
        <div className="flex items-center gap-2">
          <Label htmlFor={`${idPrefix}-${layer}-ms`} className="text-muted-foreground text-xs">
            Interval ms
          </Label>
          <NumField
            id={`${idPrefix}-${layer}-ms`}
            min={0}
            value={value.interval_ms}
            placeholder="Default backoff"
            onChange={(n) => onChange({ ...value, interval_ms: n })}
          />
          <span className="text-muted-foreground text-xs">{human ? `Fixed ${human}` : meta.defInterval}</span>
        </div>
      </div>
      {!compact && <p className="text-muted-foreground text-xs">Blank = use defaults; {meta.offHint}.</p>}
    </div>
  );
}

/** 模型配置抽屉里的三层覆盖（跟着端点走的那三层）。 */
export function ProfileRetryFields({
  value,
  onChange,
}: {
  value: LLMRetryOverride;
  onChange: (o: LLMRetryOverride) => void;
}) {
  return (
    <div className="grid gap-3 rounded-lg border p-3">
      <div className="grid gap-0.5">
        <Label className="text-sm">Retry Overrides</Label>
        <p className="text-muted-foreground text-xs">
          Applies only to this configuration, overriding the global defaults in "Retries and Backoff". Blank = inherit
          global settings; set attempts to -1 to disable retries for that layer; setting an interval replaces
          exponential backoff with a fixed interval. Circuit breaking and intent reruns are process-wide and can only be
          changed on the global settings page.
        </p>
      </div>
      {(["connect", "empty", "stream"] as const).map((k) => (
        <div key={k} className="border-t pt-3 first:border-t-0 first:pt-0">
          <RetryRuleFields
            compact
            layer={k}
            idPrefix="pf"
            value={value[k]}
            onChange={(r) => onChange({ ...value, [k]: r })}
          />
        </div>
      ))}
    </div>
  );
}

/** 「重试与退避」tab：五层的全局默认值。 */
export function RetryPolicyPanel() {
  const [policy, setPolicy] = React.useState<LLMRetryPolicy>(ZERO_POLICY);
  const [loading, setLoading] = React.useState(true);
  const [saving, setSaving] = React.useState(false);

  const load = React.useCallback(async () => {
    setLoading(true);
    try {
      const p = await api.llmRetryPolicy();
      setPolicy({ ...ZERO_POLICY, ...p });
    } catch (e) {
      toast.error(`Failed to load retry policy: ${(e as Error).message}`);
    } finally {
      setLoading(false);
    }
  }, []);

  React.useEffect(() => {
    void load();
  }, [load]);

  async function save() {
    if (saving) return;
    setSaving(true);
    try {
      // 后端会把越界值夹回区间并回传，直接用回传值刷新，所见即所存。
      const saved = await api.saveLLMRetryPolicy(policy);
      setPolicy({ ...ZERO_POLICY, ...saved });
      toast.success("Saved and effective immediately (the current call still uses the previous settings)");
    } catch (e) {
      toast.error(`Save failed: ${(e as Error).message}`);
    } finally {
      setSaving(false);
    }
  }

  const set = (k: LayerKey) => (r: LLMRetryRule) => setPolicy((p) => ({ ...p, [k]: r }));

  if (loading) {
    return (
      <div className="flex items-center gap-2 rounded-lg border border-dashed p-10 text-muted-foreground text-sm">
        <Loader2Icon className="size-4 animate-spin" /> Loading retry policy…
      </div>
    );
  }

  return (
    <div className="grid gap-4">
      <div className="rounded-lg border bg-muted/30 p-3 text-muted-foreground text-xs leading-relaxed">
        A failed model call passes through five retry layers, from inner to outer:
        <span className="text-foreground">
          {" "}
          Connection → Empty Response → Same-provider Safe Window → Rotation Circuit Breaker → Intent Rerun
        </span>
        . Outer layers run only after inner layers are exhausted, so attempt counts are
        <span className="text-foreground"> multiplied</span>. Maximizing every layer can turn one transient failure into
        dozens of requests. Leave all fields blank to use the current defaults and preserve existing behavior. The first
        three layers can be overridden per model configuration.
      </div>

      <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3">
        {(Object.keys(RETRY_LAYERS) as LayerKey[]).map((k) => (
          <RetryRuleFields key={k} layer={k} idPrefix="gl" value={policy[k]} onChange={set(k)} />
        ))}
      </div>

      <div className="flex gap-2">
        <Button onClick={save} disabled={saving}>
          {saving ? <Loader2Icon className="animate-spin" /> : <SaveIcon />}
          Save
        </Button>
        <Button variant="outline" onClick={() => setPolicy(ZERO_POLICY)} disabled={saving}>
          Reset All to Defaults
        </Button>
      </div>
    </div>
  );
}
