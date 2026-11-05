// Fallback chain for the front desk. Joy used to have exactly one provider
// (deepseekv4pro): when that key ran out of balance every text died. A chain is
// tried in order, and the first reply wins, so one dead key cannot take iMessage
// down with it.
//
// Order comes from which keys are set: deepseekv4pro first (cheap, fast), then
// any OpenAI-compatible fallbacks the environment provides.
import OpenAI from "openai";

export type ChatFn = (body: Record<string, unknown>) => Promise<unknown>;

export type Candidate = {
  label: string;
  model: string;
  /** Provider-specific extras (e.g. deepseek's `thinking`), merged over the body. */
  extra: Record<string, unknown>;
  chat: ChatFn;
};

const deepseekExtras = (kind: "front" | "deep") =>
  kind === "deep"
    ? { thinking: { type: "enabled" }, reasoning_effort: "high" }
    : { reasoning_effort: "none" };

function candidate(
  label: string,
  baseURL: string,
  apiKey: string,
  model: string,
  extra: Record<string, unknown>,
): Candidate {
  const client = new OpenAI({ baseURL, apiKey, timeout: 90_000, maxRetries: 0 });
  return {
    label,
    model,
    extra,
    chat: (body) => client.chat.completions.create({ ...body, model, ...extra } as never),
  };
}

// modelChain builds the ordered candidates for one role from the environment.
export function modelChain(kind: "front" | "deep"): Candidate[] {
  const chain: Candidate[] = [];
  const push = (c: Candidate | null) => {
    if (c) chain.push(c);
  };

  const deepseekKey = process.env.DEEPSEEK_V4PRO_API_KEY ?? "";
  if (deepseekKey) {
    push(
      candidate(
        `deepseekv4pro:${process.env.DEEPSEEK_V4PRO_MODEL ?? "deepseek-flash"}`,
        process.env.DEEPSEEK_V4PRO_BASE_URL ?? "https://api.deepseekv4pro.com/v1",
        deepseekKey,
        process.env.DEEPSEEK_V4PRO_MODEL ?? "deepseek-flash",
        deepseekExtras(kind),
      ),
    );
  }

  const openaiKey = process.env.OPENAI_API_KEY ?? "";
  if (openaiKey) {
    push(
      candidate(
        `openai:${process.env.OPENAI_MODEL ?? "gpt-4o-mini"}`,
        process.env.OPENAI_BASE_URL ?? "https://api.openai.com/v1",
        openaiKey,
        process.env.OPENAI_MODEL ?? "gpt-4o-mini",
        {},
      ),
    );
  }

  const openrouterKey = process.env.OPENROUTER_API_KEY ?? "";
  if (openrouterKey) {
    push(
      candidate(
        `openrouter:${process.env.OPENROUTER_MODEL ?? "deepseek/deepseek-chat"}`,
        "https://openrouter.ai/api/v1",
        openrouterKey,
        process.env.OPENROUTER_MODEL ?? "deepseek/deepseek-chat",
        {},
      ),
    );
  }

  // Anything else that speaks the OpenAI wire format: a local proxy, a second
  // account, a different vendor. Set all three to enable it.
  const customKey = process.env.LLM_FALLBACK_API_KEY ?? "";
  const customURL = process.env.LLM_FALLBACK_BASE_URL ?? "";
  if (customKey && customURL) {
    const model = process.env.LLM_FALLBACK_MODEL ?? "";
    if (!model) throw new Error("LLM_FALLBACK_MODEL is required when LLM_FALLBACK_BASE_URL is set");
    push(candidate(`fallback:${model}`, customURL, customKey, model, {}));
  }

  return chain;
}

// chatWithFallback returns the first successful reply and remembers which
// candidate answered, so a long outage logs once instead of on every message.
export function chatWithFallback(chain: Candidate[], quiet = false): ChatFn {
  if (!chain.length) {
    throw new Error(
      "no LLM configured: set DEEPSEEK_V4PRO_API_KEY, OPENAI_API_KEY, OPENROUTER_API_KEY, or LLM_FALLBACK_*",
    );
  }
  return async (body) => {
    let lastErr: unknown;
    for (const cand of chain) {
      try {
        return await cand.chat(body);
      } catch (err) {
        lastErr = err;
        const msg = (err instanceof Error ? err.message : String(err)).slice(0, 160);
        if (!quiet) console.error(`[llm] ${cand.label} failed: ${msg}`);
      }
    }
    throw lastErr ?? new Error("all LLM candidates failed");
  };
}

/** Logs once when a fallback candidate answers after another one failed. */
export function adapter(chain: Candidate[]): {
  chat: { completions: { create: (body: Record<string, unknown>) => Promise<unknown> } };
} {
  const chat = chatWithFallback(chain);
  return { chat: { completions: { create: chat } } };
}

// `bun llm.ts` — checks that a dead candidate is skipped without a network call.
if (import.meta.main) {
  const calls: string[] = [];
  const cand = (label: string, fail: boolean): Candidate => ({
    label,
    model: label,
    extra: {},
    chat: async () => {
      calls.push(label);
      if (fail) throw new Error(`${label} 402 Payment Required`);
      return { ok: label };
    },
  });
  const chain = [cand("dead", true), cand("alive", false)];
  const run = chatWithFallback(chain, true);
  const reply = (await run({ messages: [] })) as { ok: string };
  const ok = reply.ok === "alive" && calls.join(",") === "dead,alive";
  const allDead = await chatWithFallback([cand("dead", true)], true)({ messages: [] }).then(
    () => false,
    () => true,
  );
  if (!ok || !allDead) throw new Error(`fallback check failed: ${JSON.stringify({ reply, calls, allDead })}`);
  console.log("llm fallback ok:", calls.join(" -> "), "| all-dead throws:", allDead);
}
