# jcyber architecture

How every piece talks to every other piece. Boxes run in three places: your
phone, your Mac, and one VPS. Nothing else is required.

## The whole system

```mermaid
flowchart TB
    subgraph phone["Your phone"]
        YOU[Operator]
    end

    subgraph mac["Your Mac — joy bridge"]
        IMESSAGE[iMessage]
        FRONT["Front desk<br/>(abliterated-model, thinking off)<br/>instant acks, unfiltered dispatch"]
        SUB["Subagents<br/>(deepseek-flash, thinking on)<br/>tools: list_tasks, list_findings, jcyber_chat"]
        IMESSAGE <--> FRONT
        FRONT -- "dispatch(prompt), async" --> SUB
    end

    subgraph vps["VPS — jcyber (100.102.59.40, tailnet-only)"]
        API["Console + REST API<br/>:8787 on the tailscale IP"]
        AUTO["Auto agent<br/>(deepseek-flash, thinking high)<br/>operates the platform"]
        ENGINE["Engine<br/>planner · workers ×5 · goals"]
        PROXY["Recording proxy :8788<br/>captures every request"]
        PG[("Postgres<br/>dual knowledge graph<br/>+ evidence + settings")]
        SUB -- "tailnet HTTPS → REST" --> API
        API --> AUTO
        API --> ENGINE
        ENGINE --> PROXY
        ENGINE --> PG
        AUTO --> PG
    end

    subgraph llm["Model providers"]
        DS["DeepSeek V4.1-Flash<br/>max: worker/planner/goals<br/>high: chat roles"]
        GLM["GLM 5.3<br/>failover, priority 4"]
        ABL["Abliterated Large<br/>opt-in per agent"]
        ENGINE --> DS
        ENGINE -. "quota failover" .-> GLM
        ENGINE -. "explicit binding" .-> ABL
        FRONT --> ABL
        SUB --> DS
    end

    subgraph egress["Egress"]
        WS["Webshare residential<br/>p.webshare.io:80<br/>connection-sticky 8h"]
        TARGET[Targets]
    end

    PROXY -- "global outbound proxy" --> WS
    WS --> TARGET
    ENGINE -- "LLM traffic, direct" --> DS

    subgraph access["Access"]
        TS["Tailscale<br/>dashboard not on the public internet"]
        YOU -- "SSH tunnel / tailnet" --> TS
        TS --> API
    end
```

## A task, start to finish

```mermaid
sequenceDiagram
    participant O as Operator
    participant P as Planner
    participant W as Workers ×5
    participant G as Knowledge graph
    participant R as Report agent
    participant T as Target

    O->>P: goal (Domain dig template)
    P->>G: intents, one per attack surface
    P->>W: dispatch intents
    loop each intent, paced like a browser
        W->>T: enumerate / probe (via Webshare)
        T-->>W: responses (recorded on :8788)
        W->>G: facts, assets
    end
    Note over P,G: any fact hints at weakness →<br/>deep follow-up intents on that path<br/>(depth on promise)
    W->>R: verified finding + evidence
    R-->>O: Markdown report, bound traffic
```

## What runs where

| Where | Process | Notes |
|---|---|---|
| Phone | iMessage client | the only operator surface needed |
| Mac | `joy` bridge (`bun start`) | iMessage front desk; needs Messages logged in |
| VPS | jcyber binary | dashboard bound to the tailscale IP only |
| VPS | Postgres (docker) | schema self-migrates on boot |
| VPS | Webshare egress | set in System Settings, not env |

## Model tiers

| Tier | Model | Used by |
|---|---|---|
| max | deepseek-flash | workers, planner, goals — every reasoning turn |
| high | deepseek-flash | Auto, mainagent, reporter, retester, pentest |
| failover | glm-5.3 | automatic when DeepSeek quota dies |
| opt-in | abliterated-model-large-v2 | bind per agent when wanted; also the front desk (smallest variant) |

## Safety rails

- **Console**: tailscale-only bind; the public IP answers nothing on 8787
- **Target traffic**: always through the residential gateway, connection-sticky
- **Workers**: code-fixed stealth discipline (browser UAs, randomized pacing,
  sample-first enumeration, no full-speed mode)
- **Destructive commands**: interception rules hold them for approval
- **Tests**: run against a dedicated `jcyber_test` database, never the console
