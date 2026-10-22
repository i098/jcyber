import { Spectrum } from "spectrum-ts";
import { imessage } from "@spectrum-ts/imessage";

// Layered front desk:
//   you <-> frontdesk (fast DeepSeek, instant acks over iMessage)
//   frontdesk -> dispatch(prompt) -> subagent (DeepSeek, tools bound to jcyber)
//   subagent prompt -> jcyber (Auto agent conversation / direct reads)

const JCYBER_URL = process.env.JCYBER_URL ?? "http://127.0.0.1:8787";
const JCYBER_USER = process.env.JCYBER_USER ?? "jcyber";
const JCYBER_PASSWORD = process.env.JCYBER_PASSWORD ?? "pentest-local-2026";
const DEEPSEEK_KEY = process.env.DEEPSEEK_API_KEY ?? "";
const ABLITS_KEY = process.env.ABLITERATION_API_KEY ?? "";
// frontdesk: smallest abliterated model, thinking off — unfiltered, fast acks,
// and it doesn't hedge the operator's intent when prompting the larger models.
// subagents: DeepSeek flash, thinking on, for the real jcyber work.
const MODEL_FRONT = "abliterated-model";
const MODEL_DEEP = "deepseek-flash";
const FRONT_URL = "https://api.abliteration.ai/v1";
const DEEP_URL = "https://api.deepseek.com";

// ── types ────────────────────────────────────────────────────
interface ToolCall { id: string; function: { name: string; arguments: string } }
interface Msg {
  role: "system" | "user" | "assistant" | "tool";
  content: string | null;
  tool_calls?: ToolCall[];
  tool_call_id?: string;
  name?: string;
}
interface ToolDef { description: string; parameters: Record<string, unknown>; run: (args: Record<string, unknown>) => Promise<string> }

const record = (v: unknown): Record<string, unknown> => (v && typeof v === "object" ? v as Record<string, unknown> : {});
const str = (v: unknown, fallback = ""): string => (typeof v === "string" ? v : fallback);
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);

// ── DeepSeek ─────────────────────────────────────────────────
const TOOLS: Record<string, ToolDef> = {};

async function deepseek(model: string, url: string, key: string, messages: Msg[], tools: { type: "function"; function: { name: string; description: string; parameters: Record<string, unknown> } }[] | undefined, thinking: boolean): Promise<Msg> {
  const body: Record<string, unknown> = { model, messages, stream: false };
  // only DeepSeek accepts the thinking controls; abliteration rejects unknown params
  if (url.includes("deepseek.com")) {
    body.thinking = { type: thinking ? "enabled" : "disabled" };
    if (thinking) body.reasoning_effort = "high";
  }
  if (tools?.length) body.tools = tools;
  const res = await fetch(`${url}/chat/completions`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${key}` },
    body: JSON.stringify(body),
  });
  if (!res.ok) throw new Error(`deepseek ${res.status}: ${(await res.text()).slice(0, 300)}`);
  const data = record(record(await res.json()).choices);
  const first = record(arr(data)[0] ? record(arr(data)[0]).message : {});
  return { role: "assistant", content: str(first.content) || null, tool_calls: arr(first.tool_calls).map((tc) => {
    const c = record(tc), f = record(c.function);
    return { id: str(c.id), function: { name: str(f.name), arguments: str(f.arguments) } };
  }) };
}

function toolSpecs() {
  return Object.entries(TOOLS).map(([name, t]) => ({ type: "function" as const, function: { name, description: t.description, parameters: t.parameters } }));
}

// ── jcyber REST ──────────────────────────────────────────────
let token: string | null = null, tokenAt = 0;
async function auth(): Promise<string> {
  if (token && Date.now() - tokenAt < 6 * 3600e3) return token;
  const res = await fetch(`${JCYBER_URL}/api/auth/login`, {
    method: "POST", headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ username: JCYBER_USER, password: JCYBER_PASSWORD }),
  });
  if (!res.ok) throw new Error(`jcyber login ${res.status}`);
  token = str(record(await res.json()).token); tokenAt = Date.now();
  return token;
}
async function jget(path: string): Promise<unknown> {
  const res = await fetch(`${JCYBER_URL}${path}`, { headers: { Authorization: `Bearer ${await auth()}` } });
  if (!res.ok) throw new Error(`GET ${path} ${res.status}`);
  return res.json();
}

let autoConv: string | null = null;
async function jcyberChat(message: string): Promise<string> {
  if (!autoConv) {
    const res = await fetch(`${JCYBER_URL}/api/conversations`, {
      method: "POST",
      headers: { "Content-Type": "application/json", Authorization: `Bearer ${await auth()}` },
      body: JSON.stringify({ agent_key: "auto", title: "front desk subagent" }),
    });
    if (!res.ok) throw new Error(`conv ${res.status}`);
    autoConv = str(record(await res.json()).id);
  }
  const res = await fetch(`${JCYBER_URL}/api/conversations/${autoConv}/messages`, {
    method: "POST",
    headers: { "Content-Type": "application/json", Authorization: `Bearer ${await auth()}` },
    body: JSON.stringify({ message }),
  });
  if (!res.ok) throw new Error(`send ${res.status}: ${(await res.text()).slice(0, 200)}`);
  const d = record(await res.json());
  return str(d.reply) || str(d.content) || str(d.text) || JSON.stringify(d).slice(0, 800);
}

// subagent tools: fast reads + the jcyber Auto agent (which operates the platform)
TOOLS.jcyber_chat = {
  description: "Send an instruction or question to the jcyber operator agent. It can list/create/pause tasks, read findings, inject hints, and manage the platform. Returns its reply.",
  parameters: { type: "object", properties: { prompt: { type: "string", description: "Precise instruction for jcyber" } }, required: ["prompt"] },
  run: (a) => jcyberChat(str(a.prompt)),
};
TOOLS.list_tasks = {
  description: "List jcyber tasks with status (fast direct read).",
  parameters: { type: "object", properties: {} },
  run: async () => {
    const d = record(await jget("/api/tasks"));
    return JSON.stringify(arr(d.tasks ?? d).slice(0, 15).map((t) => {
      const r = record(t);
      return { id: r.id, name: str(r.name), status: str(r.status), paused: r.paused };
    }));
  },
};
TOOLS.list_findings = {
  description: "List recent verified findings with severity (fast direct read).",
  parameters: { type: "object", properties: {} },
  run: async () => {
    const d = record(await jget("/api/exploration/findings"));
    return JSON.stringify(arr(d.findings ?? d).slice(0, 15).map((f) => {
      const r = record(f);
      return { id: r.id, severity: str(r.severity), name: str(r.name) || str(r.summary), state: str(r.status) || str(r.state) };
    }));
  },
};

// ── subagent ─────────────────────────────────────────────────
async function subagent(prompt: string): Promise<string> {
  const sys = `You are a jcyber operations subagent. Fulfill this operator request using your tools.
Prefer list_tasks/list_findings for plain reads; use jcyber_chat for anything that changes state
(create/pause tasks, hints, worker messages) or needs platform reasoning.
Return a concise factual summary of what happened and what was found.`;
  const msgs: Msg[] = [{ role: "system", content: sys }, { role: "user", content: prompt }];
  for (let turn = 0; turn < 8; turn++) {
    const m = await deepseek(MODEL_DEEP, DEEP_URL, DEEPSEEK_KEY, msgs, toolSpecs(), true);
    if (!m.tool_calls?.length) return m.content ?? "(no summary)";
    msgs.push(m);
    for (const tc of m.tool_calls) {
      const out = await TOOLS[tc.function.name]?.run(JSON.parse(tc.function.arguments || "{}")).catch((e: unknown) => `error: ${e}`);
      msgs.push({ role: "tool", tool_call_id: tc.id, name: tc.function.name, content: out ?? "ok" });
    }
  }
  return "(subagent hit turn limit)";
}

// ── frontdesk ────────────────────────────────────────────────
const FRONT_SYS = `You are the front desk for jcyber, an autonomous testing console, reached over iMessage.
Style: short, plain, no emoji spam. Answer the operator directly when you can from context.
For anything needing live jcyber state or action, call dispatch with a precise subagent prompt —
then immediately acknowledge ("on it") and stop; the result follows as a separate message.`;
const dispatch = { type: "function" as const, function: { name: "dispatch", description: "Spawn a subagent to interact with jcyber and report back", parameters: { type: "object", properties: { prompt: { type: "string", description: "What the subagent should find out or do in jcyber" } }, required: ["prompt"] } } };

const app = await Spectrum({
  projectId: process.env.PROJECT_ID!,
  projectSecret: process.env.PROJECT_SECRET!,
  providers: [imessage.config()],
});

for await (const [space, message] of app.messages) {
  if (message.content.type !== "text") continue;
  const text = message.content.text.trim();
  if (!text) continue;
  try {
    const m = await deepseek(MODEL_FRONT, FRONT_URL, ABLITS_KEY, [{ role: "system", content: FRONT_SYS }, { role: "user", content: text }], [dispatch], false);
    const call = m.tool_calls?.[0];
    if (call?.function.name === "dispatch") {
      const args = record(JSON.parse(call.function.arguments || "{}"));
      await space.send(m.content?.trim() || "on it");
      // read receipts aren't exposed by the provider; typing shows the work
      space.startTyping?.().catch?.(() => {});
      void subagent(str(args.prompt))
        .then(async (r) => { await space.stopTyping?.().catch?.(() => {}); await space.send(`jcyber: ${r}`); })
        .catch(async (e: unknown) => { await space.stopTyping?.().catch?.(() => {}); await space.send(`jcyber error: ${e instanceof Error ? e.message : String(e)}`); });
    } else {
      await space.send(m.content || "…");
    }
  } catch (err) {
    await space.send(`frontdesk error: ${err instanceof Error ? err.message : String(err)}`);
  }
}
