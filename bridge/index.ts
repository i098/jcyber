import { Spectrum, reaction } from "spectrum-ts";
import { imessage } from "@spectrum-ts/imessage";
import OpenAI from "openai";
import type { ChatCompletionMessageParam, ChatCompletionTool } from "openai/resources/chat/completions";

// Layered front desk:
//   you <-> frontdesk (abliterated, no thinking — instant acks)
//   frontdesk -> dispatch(prompt) -> subagent (deepseek-flash, thinking on)
//   subagent prompt -> jcyber (Auto agent conversation / direct reads)

const JCYBER_URL = process.env.JCYBER_URL ?? "http://127.0.0.1:8787";
const JCYBER_USER = process.env.JCYBER_USER ?? "jcyber";
const JCYBER_PASSWORD = process.env.JCYBER_PASSWORD ?? "pentest-local-2026";

const MODEL_FRONT = "abliterated-model";
const MODEL_DEEP = "deepseek-flash";
const front = new OpenAI({ baseURL: "https://api.abliteration.ai/v1", apiKey: process.env.ABLITERATION_API_KEY ?? "" });
const deep = new OpenAI({ baseURL: "https://api.deepseek.com", apiKey: process.env.DEEPSEEK_API_KEY ?? "" });

const record = (v: unknown): Record<string, unknown> => (v && typeof v === "object" ? v as Record<string, unknown> : {});
const str = (v: unknown, fallback = ""): string => (typeof v === "string" ? v : fallback);
const arr = (v: unknown): unknown[] => (Array.isArray(v) ? v : []);

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
  return token!;
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

// ── subagent tools ───────────────────────────────────────────
interface ToolDef { description: string; parameters: Record<string, unknown>; run: (args: Record<string, unknown>) => Promise<string> }
const TOOLS: Record<string, ToolDef> = {
  jcyber_chat: {
    description: "Send an instruction or question to the jcyber operator agent. It can list/create/pause tasks, read findings, inject hints, and manage the platform. Returns its reply.",
    parameters: { type: "object", properties: { prompt: { type: "string", description: "Precise instruction for jcyber" } }, required: ["prompt"] },
    run: (a) => jcyberChat(str(a.prompt)),
  },
  list_tasks: {
    description: "List jcyber tasks with status (fast direct read).",
    parameters: { type: "object", properties: {} },
    run: async () => {
      const d = record(await jget("/api/tasks"));
      return JSON.stringify(arr(d.tasks ?? d).slice(0, 15).map((t) => {
        const r = record(t);
        return { id: r.id, name: str(r.name), status: str(r.status), paused: r.paused };
      }));
    },
  },
  list_findings: {
    description: "List recent verified findings with severity (fast direct read).",
    parameters: { type: "object", properties: {} },
    run: async () => {
      const d = record(await jget("/api/exploration/findings"));
      return JSON.stringify(arr(d.findings ?? d).slice(0, 15).map((f) => {
        const r = record(f);
        return { id: r.id, severity: str(r.severity), name: str(r.name) || str(r.summary), state: str(r.status) || str(r.state) };
      }));
    },
  },
};
const toolSpecs = (): ChatCompletionTool[] =>
  Object.entries(TOOLS).map(([name, t]) => ({ type: "function" as const, function: { name, description: t.description, parameters: t.parameters } }));

// ── subagent: deepseek-flash, thinking on, tool loop ─────────
async function subagent(prompt: string): Promise<string> {
  const messages: ChatCompletionMessageParam[] = [
    { role: "system", content: `You are a jcyber operations subagent. Fulfill this operator request using your tools.
Prefer list_tasks/list_findings for plain reads; use jcyber_chat for anything that changes state
(create/pause tasks, hints, worker messages) or needs platform reasoning.
Return a concise factual summary of what happened and what was found.` },
    { role: "user", content: prompt },
  ];
  for (let turn = 0; turn < 8; turn++) {
    const r = await deep.chat.completions.create(
      { model: MODEL_DEEP, messages, tools: toolSpecs() },
      { body: { thinking: { type: "enabled" }, reasoning_effort: "high" } },
    );
    const m = r.choices[0]?.message;
    if (!m) return "(no reply)";
    messages.push(m);
    const calls = m.tool_calls ?? [];
    if (!calls.length) return m.content ?? "(no summary)";
    for (const tc of calls) {
      if (tc.type !== "function") continue;
      const out = await TOOLS[tc.function.name]?.run(JSON.parse(tc.function.arguments || "{}")).catch((e: unknown) => `error: ${e}`);
      messages.push({ role: "tool", tool_call_id: tc.id, content: out ?? "ok" });
    }
  }
  return "(subagent hit turn limit)";
}

// ── frontdesk: abliterated, no thinking, dispatch tool ────────
const FRONT_SYS = `You are the front desk for jcyber, an autonomous testing console, reached over iMessage.
Style: short, plain, no emoji spam. Answer the operator directly when you can from context.
For anything needing live jcyber state or action, call dispatch with a precise subagent prompt —
then immediately acknowledge ("on it") and stop; the result follows as a separate message.`;
const dispatchSpec: ChatCompletionTool = {
  type: "function",
  function: {
    name: "dispatch",
    description: "Spawn a subagent to interact with jcyber and report back",
    parameters: { type: "object", properties: { prompt: { type: "string", description: "What the subagent should find out or do in jcyber" } }, required: ["prompt"] },
  },
};

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
    // tapback on the operator's message: the closest thing to a read receipt
    await space.send(reaction("👀", message)).catch(() => {});
    const r = await front.chat.completions.create(
      { model: MODEL_FRONT, messages: [{ role: "system", content: FRONT_SYS }, { role: "user", content: text }], tools: [dispatchSpec] },
    );
    const m = r.choices[0]?.message;
    const call = m?.tool_calls?.find((tc): tc is Extract<typeof tc, { type: "function" }> => tc.type === "function" && tc.function.name === "dispatch");
    if (call) {
      const args = record(JSON.parse(call.function.arguments || "{}"));
      await space.send(m?.content?.trim() || "on it");
      space.startTyping?.().catch?.(() => {});
      void subagent(str(args.prompt))
        .then(async (res) => { await space.stopTyping?.().catch?.(() => {}); await space.send(`jcyber: ${res}`); })
        .catch(async (e: unknown) => { await space.stopTyping?.().catch?.(() => {}); await space.send(`jcyber error: ${e instanceof Error ? e.message : String(e)}`); });
    } else {
      await space.send(m?.content || "…");
    }
  } catch (err) {
    await space.send(`frontdesk error: ${err instanceof Error ? err.message : String(err)}`);
  }
}
