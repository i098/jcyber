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

// ── frontdesk: abliterated, no thinking, UniiChat memory ──────
import { UniChat } from "./unichat";
const memory = new UniChat(
  process.env.MEMORY_DIR ?? "data",
  front, // the cheap model summarizes the log
  MODEL_FRONT,
);

const REACTIONS = ["eyes", "thumbs up", "check", "cross", "fire", "thinking", "exclamation"] as const;
const REACTION_EMOJI: Record<string, string> = { eyes: "👀", "thumbs up": "👍", check: "✅", cross: "❌", fire: "🔥", thinking: "🤔", exclamation: "❗️" };
const FRONT_SYS = `You are the front desk for jcyber, an autonomous testing console, reached over iMessage, in a single chat that never ends.

Your memory is the view inside <chat>: one-line summaries of the whole conversation so far, oldest first, as id+n|text. Recent lines cover one message each; older lines cover more. Its latest word on a thing is the truth. Whenever you need any information, first find its latest mention in the view and zoom until you have it whole, before you act, guess or ask. zoom(id, n) opens a line into the two lines under it; zoom(id, 1) gives the message whole; date(id) gives when it was sent. Say in your reply what you learned that will matter later.

Message kinds: user (the operator's texts), front (your replies), tool/echo (your tool calls and results), work (a jcyber subagent report, starting "[jcyber]"). A subagent's report may arrive as a later message; never wait for one — acknowledge, end, and the result follows.

Style: short, plain, no emoji spam. Answer the operator directly when you can from the view. Reacting: you may call react once with a label from the library (${REACTIONS.join(", ")}) — sparingly, only when a tapback adds meaning (eyes when you dispatch work, check for done); most messages need none. For anything needing live jcyber state or action, call dispatch with a precise subagent prompt, acknowledge ("on it"), and stop; the result follows as a separate message.`;
const dispatchSpec: ChatCompletionTool = {
  type: "function",
  function: {
    name: "dispatch",
    description: "Spawn a subagent to interact with jcyber and report back",
    parameters: { type: "object", properties: { prompt: { type: "string", description: "What the subagent should find out or do in jcyber" } }, required: ["prompt"] },
  },
};
const reactSpec: ChatCompletionTool = {
  type: "function",
  function: {
    name: "react",
    description: "Attach a tapback reaction to the operator's message",
    parameters: { type: "object", properties: { label: { type: "string", enum: [...REACTIONS], description: "Reaction from the library" } }, required: ["label"] },
  },
};
const zoomSpec: ChatCompletionTool = {
  type: "function",
  function: {
    name: "zoom",
    description: "Open the line id+n of the view into the two lines of n/2 under it; n = 1 gives the message whole",
    parameters: { type: "object", properties: { id: { type: "number" }, n: { type: "number", description: "Power of 2; id must be a multiple of n" } }, required: ["id", "n"] },
  },
};
const dateSpec: ChatCompletionTool = {
  type: "function",
  function: { name: "date", description: "The date and time of message id", parameters: { type: "object", properties: { id: { type: "number" } }, required: ["id"] } },
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
    const userMsgId = memory.append("user", text);
    // a turn waits until everything before the new message is summarized
    await memory.ensureSummarized(userMsgId);
    const messages: ChatCompletionMessageParam[] = [
      { role: "system", content: FRONT_SYS },
      { role: "user", content: `${await memory.viewBlock()}\n\n${text}` },
    ];
    let dispatchPrompt: string | null = null;
    let reply = "";
    for (let turn = 0; turn < 5; turn++) {
      const r = await front.chat.completions.create(
        { model: MODEL_FRONT, messages, tools: [dispatchSpec, reactSpec, zoomSpec, dateSpec] },
      );
      const m = r.choices[0]?.message;
      if (!m) break;
      messages.push(m);
      if (m.content) memory.append("front", m.content);
      const calls = m.tool_calls ?? [];
      if (!calls.length) { reply = m.content ?? ""; break; }
      let dispatched = false;
      for (const tc of calls) {
        if (tc.type !== "function") continue;
        const args = record(JSON.parse(tc.function.arguments || "{}"));
        memory.append("tool", `${tc.function.name}(${tc.function.arguments ?? ""})`);
        let out: string;
        if (tc.function.name === "react") {
          const emoji = REACTION_EMOJI[str(args.label)] ?? "👀";
          await space.send(reaction(emoji, message)).catch(() => {});
          out = `reacted ${emoji}`;
        } else if (tc.function.name === "zoom") {
          out = await memory.zoom(Number(args.id), Number(args.n) || 1);
        } else if (tc.function.name === "date") {
          out = memory.dateOf(Number(args.id));
        } else if (tc.function.name === "dispatch") {
          dispatchPrompt = str(args.prompt);
          dispatched = true;
          out = "dispatched";
        } else {
          out = "unknown tool";
        }
        memory.append("echo", out.slice(0, 4_000));
        messages.push({ role: "tool", tool_call_id: tc.id, content: out });
      }
      if (dispatched) { reply = m.content ?? "on it"; break; }
    }
    if (dispatchPrompt !== null) {
      const prompt = dispatchPrompt;
      await space.send(reply.trim() || "on it");
      space.startTyping?.().catch?.(() => {});
      void subagent(prompt)
        .then(async (res) => {
          await space.stopTyping?.().catch?.(() => {});
          memory.append("work", `[jcyber] ${res}`);
          await space.send(`jcyber: ${res}`);
        })
        .catch(async (e: unknown) => {
          await space.stopTyping?.().catch?.(() => {});
          memory.append("work", `[jcyber] error: ${e instanceof Error ? e.message : String(e)}`);
          await space.send(`jcyber error: ${e instanceof Error ? e.message : String(e)}`);
        });
    } else {
      await space.send(reply.trim() || "…");
    }
  } catch (err) {
    await space.send(`frontdesk error: ${err instanceof Error ? err.message : String(err)}`);
  }
}
