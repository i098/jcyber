# Phone front desk — Photon/Spectrum setup

The front desk is a phone number you text, backed by a layered agent: fast
unfiltered acks, DeepSeek subagents doing the real work against jcyber. The
bridge lives in [`bridge/`](bridge/).

```
your phone ⇄ SMS/iMessage ⇄ Photon line
                              ⇄ bridge on the VPS (bun, 24/7)
                                 frontdesk: abliterated-model, thinking off
                                 subagents: deepseek-flash, thinking on
                                    ⇄ jcyber REST (Auto agent + reads)
```

## 1. Create the Spectrum project

On [app.photon.codes](https://app.photon.codes), create a project with the
iMessage platform. Then scaffold the bridge anywhere:

```bash
bun create spectrum-project@latest <name> --projectId <id> --providers imessage --yes
```

The repo already carries the finished bridge code in `bridge/` — you only need
its `.env` (see [`bridge/.env.example`](bridge/.env.example)):

```bash
cp bridge/.env.example bridge/.env   # fill PROJECT_ID + PROJECT_SECRET from the dashboard
```

## 2. Install the Photon CLI on the VPS

The CLI is an npm package and needs node:

```bash
sudo apt-get install -y nodejs npm
bun add -g @photon-ai/cli
photon login          # device flow: open the URL it prints, enter the code
photon whoami         # should show your account
```

## 3. Get your number (shared line, free tier)

Dedicated lines are a Business-plan feature, but the free tier assigns each
user a number from the managed pool. Create yourself as a user:

```bash
curl -s -X POST "https://spectrum.photon.codes/projects/$PROJECT_ID/users/" \
  -u "$PROJECT_ID:$PROJECT_SECRET" \
  -H "Content-Type: application/json" \
  -d '{"type":"shared","phoneNumber":"+1XXXXXXXXXX","firstName":"You"}'
```

The `assignedPhoneNumber` in the response is your front desk number.
Note: `photon spectrum users add` currently sends a wrong field name
(`phone` instead of `phoneNumber`) and fails — the direct API call above is
the working path.

## 4. Run the bridge

On the VPS (or any always-on box with bun):

```bash
cd bridge && bun install
nohup bun start > bridge.log 2>&1 &
```

Point `JCYBER_URL` at the console (tailnet URL if the console binds the
tailscale interface, `http://127.0.0.1:8787` if same box).

## 5. Text it

From the phone number you registered, message the assigned number:

- `status` → instant ack, then the task list from jcyber
- `create a domain dig task for example.com` → subagent drives the console
- anything else → the front desk decides: answer directly or dispatch

## Cost

Free tier: the number and unlimited daily messages (with Auto Scale) cost
nothing. Dedicated lines and group messaging are paid tiers; see
[photon.codes/pricing](https://photon.codes/pricing).
