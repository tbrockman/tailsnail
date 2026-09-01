# tailsnail

Peer-to-peer terminal Snake, played over your own tailnet.

`tsnail` is a single Go binary with an embedded Tailscale node. Every instance
is a full peer: it joins your tailnet as its own device, finds other players by
probing them directly, hosts or joins lobbies, runs matches, and gossips signed
match results with everyone it meets. There is no server, no account, and
nothing to deploy — if two people on the same tailnet run `tsnail`, they can
see each other.

```
tailsnail  │  lobby   friday night  •  40×20  wrap  20 ticks/s  classic     ● tsnail-laptop 100.64.1.2
────────────────────────────────────────────────────────────────────────────────────────────────────
   ● ada 7      ◆ grace 12      ■ hedy 5      ▲ katherine 9

   ╭────────────────────────────────────────╮
   │              ░▒▓███◆                   │
   │                                 ◎      │
   │      ●███▓▒░                           │
   │                        ▓▓██■           │
   │   ◎                                    │
   │                ▲██▓░                   │
   ╰────────────────────────────────────────╯
────────────────────────────────────────────────────────────────────────────────────────────────────
↑↓←→ / wasd steer  •  esc leave  •  ctrl+l logs
```

## What it does

- **Joins your tailnet as a device.** One browser visit on first run, none ever
  again. No `tailscaled`, no `tailscale` CLI, no root.
- **Finds peers by probing them.** Every online node in your netmap is a
  candidate; the ones that answer a tailsnail handshake show up in the lobby
  browser within a couple of seconds.
- **Runs host-authoritative matches** for two to eight players, with two
  gameplay modes and configurable arena, speed and wrap-around. Seats can be
  filled with bots, so a lobby is playable without waiting for anybody.
- **Signs and gossips results.** Every participant signs the final standings,
  and peers sync each other's match history whenever they connect — so a
  leaderboard assembles itself with no central store.

## Install and run

### Nix

```sh
nix run github:theolol/tailsnail       # run it
nix build github:theolol/tailsnail     # ./result/bin/tsnail
nix develop                            # dev shell: go, gopls, staticcheck, delve
```

The flake provides `packages.default`, an app, and a dev shell for both
`aarch64-darwin` and `x86_64-linux`.

### Plain Go

Requires Go 1.26.6 or newer (a `tailscale.com` dependency sets the floor).

```sh
go build -o tsnail ./cmd/tsnail
./tsnail
```

### Command line

```
tsnail [flags]                 launch the game
tsnail history export --json   print stored match records as JSON

  -hostname string     tailnet hostname for this node (default tsnail-<hostname>)
  -state-dir string    directory for node state, identity and match history
  -verbose             also mirror the in-app log to a file in the state directory
  -ascii               draw with plain ASCII instead of Unicode glyphs
  -color string        colour depth: auto|truecolor|256|16|none (default "auto")
  -emoji string        use the snail icon: auto|on|off (default "auto")
  -version             print the version and exit
```

Keys that work everywhere: `,` opens settings, `ctrl+l` shows the captured
log, `q` quits. Each screen's help bar lists the rest.

`NO_COLOR` is honoured and overrides `--color`.

Emoji detection is a heuristic — there is no capability query for it — so
`auto` requires a UTF-8 locale *and* a terminal known to render emoji at the
width it advertises. Anything unrecognised falls back to a plain spiral glyph
rather than risking a mis-measured cell, which would shear every column after
it. It is not a preference the app asks about, only a capability it detects;
`--emoji` is there for when the detection is wrong.

## First run, step by step

This is the part the project exists to get right. What you actually see:

**1. You run `tsnail`.** The wordmark appears — an animated snake drawn with
the same glyphs and the same shimmer the game uses — under a spinner reading
*starting the embedded Tailscale node*.

**2. A moment later, the authorisation screen.** tsnet has contacted the
control plane and published a browse-to URL on the IPN bus. tailsnail is
watching that bus, so instead of the URL being dumped to stderr and shredding
the display, you get a screen explaining what is about to happen:

```
                          ●████████▓▓

                       t a i l s n a i l
             peer-to-peer snake over your tailnet

        tailsnail joins your tailnet as its own device,
            so other players can find you directly.

      Authorise it once. Every launch after this connects
          straight away with no browser and no prompt.

  ╭──────────────────────────────────────────────────────────╮
  │ https://login.tailscale.com/a/0123456789abcdef            │
  ╰──────────────────────────────────────────────────────────╯

          ⠹ waiting for you to authorise this device…
                 opening this in your browser…
```

Your browser opens automatically (`open` on macOS, `xdg-open` on Linux). If it
does not — over SSH, in a container, on a headless box — the URL is right
there, wrapped rather than truncated so you can read or copy all of it. Press
`o` to try the browser again.

**3. You approve the device.** The screen switches to a brief success beat
showing which device you came up as, then the main menu:

```
  ✓ you're on the tailnet

  device   tsnail-laptop
  address  100.64.1.2
  account  ada@example.com
  tailnet  example.com
```

**4. Every run after that.** The node key is persisted under the state
directory, so `Up()` completes without a prompt. You get a second or two of
*connecting to your tailnet…* and land on the menu. The authorisation screen
never flashes.

### When it does not go smoothly

Each of these is a real screen with an explanation, not a spinner that hangs:

| Situation | What you see |
|---|---|
| No network | *cannot reach your tailnet*, the backend's own error, and a note that it keeps retrying. Health warnings from the Tailscale backend (unreachable DERP, no internet) are surfaced verbatim. |
| Tailnet requires device approval | *this tailnet requires new devices to be approved* — naming the device an admin needs to approve. Detected from `ipn.NeedsMachineAuth`. |
| Node key expired or revoked | *this device is logged out*, with `ctrl+r` to sign in again. The same onboarding screen, framed so you know why it came back. |
| Login taken too long | The screen stays up; `ctrl+r` restarts the interactive login. |

None of this reaches your terminal as log spam. Every line tsnet writes goes
into a sanitising ring buffer — ANSI escapes and control characters stripped on
capture — viewable with `ctrl+l` and mirrored to `tsnail.log` under
`--verbose`.

## State on disk

Everything lives under `os.UserConfigDir()/tsnail`, created `0700`:

```
tsnet/            embedded Tailscale node state (the persisted node key)
identity.json     this install's ed25519 signing key and display name (0600)
settings.json     theme, glyphs, colour, last hosted configuration
matches/*.json    one attested match record per file
tsnail.log        verbose log mirror (only with --verbose)
```

Override the location with `--state-dir`.

## How discovery works

**Port 41649/tcp**, bound only to the node's tailnet addresses via
`tsnet.Server.Listen`. Nothing ever binds a public interface. The port sits
clear of Tailscale's own WireGuard port (41641) while staying next to it.

Discovery has no registry and no broadcast:

1. Poll `LocalClient.Status` every two seconds. Every online peer with a
   tailnet address is a *candidate*.
2. Dial each due candidate on the well-known port and exchange
   `Hello`/`HelloOK` with a 2.5 second timeout. A peer that answers with a
   matching app name and protocol version is a tailsnail peer; its handshake
   carries its display name, signing key, and the lobby it is hosting, if any.
3. Cache the result. A peer that answered is re-probed after 4 seconds, so a
   lobby filling up or closing shows within a few seconds. A node that did not
   answer backs off exponentially from 20 seconds to 2 minutes — a tailnet full
   of laptops that will never run tailsnail costs almost nothing to keep
   scanning.
4. A change in the set of online nodes forces an immediate sweep, since that is
   exactly when a lobby is likely to have appeared.

Probes fan out at most eight at a time. Inbound connections are identified with
`LocalClient.WhoIs`, which is how the roster shows the tailnet user and device
behind each player.

## The protocol

JSON messages, each framed with a 4-byte big-endian length prefix, capped at
4 MiB. One port serves every interaction; the first message's `intent` field
decides what happens next.

```
probe   → Hello/HelloOK, then optionally one gossip round, then close
play    → Hello/HelloOK, JoinLobby/JoinOK, then a persistent session
gossip  → Hello/HelloOK, then anti-entropy
```

| Message | Direction | Purpose |
|---|---|---|
| `hello` / `hello_ok` | both | Handshake: app, protocol version, signing key, display name, lobby advert |
| `join_lobby` / `join_ok` | client → host | Take a seat; the host assigns a seat index and palette slot |
| `lobby_state` | host → clients | Full roster, phase, config, settings generation, activity feed, countdown |
| `ready` | client → host | Toggle ready, quoting the settings generation it was decided under |
| `leave` / `kick` | both | Voluntary departure; host-initiated removal |
| `start` | host → clients | Match beginning, with config, roster and your seat |
| `input` | client → host | A heading change plus a client tick stamp |
| `tick_state` | host → clients | One authoritative snapshot, plus the highest client tick applied |
| `game_over` | host → clients | Final ranked state |
| `attest_request` / `attestation` | both | The result to sign; the signature |
| `attested_record` | host → clients | The assembled record with all signatures collected |
| `gossip_inv` / `gossip_resp` / `gossip_records` | both | Anti-entropy |
| `ping` / `pong` | both | Heartbeats |
| `error` | both | A refusal, with a code |

Full state is broadcast every tick rather than deltas. At these grid sizes it
is a few kilobytes — a 120×48 arena with eight long snakes is well under the
frame cap — and a full snapshot is idempotent, which is what makes it safe to
drop frames for a lagging client instead of stalling everyone.

## Consensus: host-authoritative, no lockstep

The host runs the only simulation, at the configured tick rate. Clients send
inputs and render what the host last broadcast. Correctness never depends on
clients agreeing about anything.

**Why this and not lockstep or rollback.** On a tailnet the connection is
usually direct WireGuard, so latency is low and stable. Lockstep would make
every player wait for the slowest; rollback netcode would add real complexity
to hide a problem that does not exist at this latency. The trade-off taken here
is that the host has authority and the host is a single point of failure —
which is acceptable for a game among people who already share a tailnet.

**Prediction.** A client applies its own turn locally, but only in the last
40% of the move window, so the predicted cell appears a few milliseconds before
the host's own version of it. A turn feels instant and the correction is
invisible. A prediction that would leave a walled arena is declined rather than
drawn, because a snake briefly shown through a wall reads as a bug.

**Fault tolerance.**

- The host pings every client every 2 seconds; clients ping back.
- A client silent for 4 seconds is marked disconnected. Its snake *coasts* —
  keeps travelling straight, still a hazard, still killable — and greys out in
  the arena and roster so the table can see it is on rails.
- Silent for 10 seconds and the seat is eliminated and dropped.
- A client that reconnects within the window is picked straight back up.
- **Leaving a match in progress forfeits it**: the snake is eliminated rather
  than left coasting, so the board reflects who is still playing. Results are
  built from the roster as it stood at kickoff, so someone who walks out still
  appears in the record with the placement they earned — unsigned, which is
  what makes the record partially attested.
- A slow client loses tick frames rather than stalling the match; each client
  has its own writer goroutine and a bounded outbox.

**If the host dies, the match ends for everyone.** Clients get a clear dialog
and return to the lobby browser. **Host migration is a non-goal** — it would
mean replicating simulation state and electing a new authority, which is a
large amount of machinery for a case where someone can simply open a new lobby.

## Lobbies and gameplay

A host configures grid size, tick rate, snake speed, seats (2–8), bots,
wrap-around, food count, and mode. Players join from the browser, see the
roster with each player's colour and glyph, and toggle ready. **The match
starts automatically once every seated player is ready**, after an animated
3-2-1. Readying up alone is a legitimate practice mode.

The board goes up as the countdown begins, not when it ends: three seconds to
find your own snake and see where everyone else starts is the difference
between reacting on the first tick and spending it working out which one is
you. Nothing moves until the count reaches zero, and snakes spawn clear of the
middle so the digit does not cover one. Un-readying or leaving during the
countdown aborts it, because the board everyone is looking at would no longer
be the board that gets played.

The host can change a running lobby's settings in place with `e`, without
tearing the room down. Doing so un-readies everyone, since they agreed to the
previous configuration — and a ready that crossed paths with a change is
refused rather than applied, so nobody is committed to settings they never
saw.

**Bots** fill seats with computer players, which is how you play alone or test
a change. They are seated in the lobby rather than conjured at kickoff, so the
roster shows who will actually play and joins are limited to the seats left
over. A bot is always ready and never holds a lobby up; a lobby of nothing but
bots cannot start. The policy is deliberately simple and readable rather than
strong: avoid the fatal move, prefer not to meet another head, then head for
the nearest pellet, then keep options open. Bots have no signing key, so they
are not participants in a match record — the count is recorded in the config
instead, so a result never reads as a full field of people.

**Themes are a viewer setting, modes are a host setting.** A theme only changes
how your own terminal draws the game, so it lives in settings and every player
picks their own; the gameplay variant changes what the simulation does, so it
belongs to the host's lobby configuration. Two modes:

- **classic** — fixed arena, last snake standing wins.
- **shrinking** — the arena contracts by one ring every *N* moves, forcing
  survivors together. The swallowed ground is drawn as closed, and the border
  flashes as the walls close in.

Collisions resolve simultaneously against the pre-move world, so two snakes
entering the same cell both die rather than the one earlier in seat order
winning. Chasing your own vacating tail is legal. Driving into another snake's
body credits them a kill; a head-on credits nobody.

After a match the lobby reopens with everyone un-readied, so a group can keep
playing without rebuilding it.

## Attestation and gossip

Peers are trusted. Signatures exist for provenance and for a verifiable ranked
history, not as a defence against a malicious peer — **tailnet ACLs are the
security boundary**. There is no anti-cheat.

**Identity.** On first run each install generates an ed25519 keypair, stored
beside the tsnet state. Players are identified by that key, so renaming
yourself carries your history forward rather than splitting it.

**The record.** When a match ends the host builds a `MatchResult`: match ID
(UUID v4), full config, start and end timestamps, the participant list binding
each signing key to the tailnet identity `WhoIs` reported and the seat played,
and placements with per-player length, food, kills and survival ticks.

**Canonicalisation.** The result is marshalled to JSON, decoded to generic
values, and re-encoded with every object key sorted and no insignificant
whitespace. The hash therefore survives struct field reordering, a different Go
version, and a round trip through any other JSON implementation. Numbers keep
their exact literal form via `json.Number`. The digest is
`sha256("tailsnail/match-result/v1\n" || canonical)`.

**Signing.** Every participant signs that digest and returns the signature; the
host assembles `MatchResult + signatures[]` into an attested record and
distributes it. Signatures are stored sorted by key, and a signature from a key
that did not play is rejected. If someone drops before signing, the record is
still stored and marked *partially attested* — visible as `partial 2/4` on the
results and history screens.

**Gossip.** Whenever two peers connect — including the ordinary discovery
probe, which holds its connection open for one round — they exchange
inventories and sync what the other lacks:

```
dialer   → listener   gossip_inv{[{match_id, hash, sigs}]}
listener → dialer     gossip_resp{want:[ids], records:[...]}
dialer   → listener   gossip_records{records:[...]}
```

The inventory carries a signature *count* as well as a hash, so a peer holding
the same result with more signatures is also worth pulling — partially attested
records converge, not just missing ones. Records are verified before being
accepted, and an existing record is only ever rewritten to *add* signatures,
never to change what it attests to. Batches are capped at 48 records per
exchange; a peer further behind catches up over several connections. The third
message is skipped when the listener wants nothing, so a steady-state sync
between converged peers is two messages.

The result: match history and the leaderboard assemble themselves just by
people running the app.

## Interface

Screens: onboarding, menu, lobby browser, host form, lobby room, arena,
results, history and leaderboard, settings, plus an activity dialog and a
debug log overlay. Settings are reachable from any of them with `,`; enter
saves and escape discards, and because theme and glyph changes apply live so
they can be judged, discarding actively restores the previous look.

Anywhere a list describes its selection — the menu, both host and settings
forms, the lobby browser, the leaderboard and the match list — the description
is a popover attached to the text it describes, the way a tooltip attaches to
an element rather than to the page. It overlaps whatever is beneath, and falls
through beside → below → above → left until one placement fits the space
actually available, so a popover never simply disappears because the window is
an awkward shape. Left is last because it is the only placement that covers
the very text being described. Descriptions are never inline underneath the
selection: that changes the container's height as the selection moves, so
every row below shifts by a line on each keypress and the list becomes
impossible to scan. Every form field is guaranteed exactly one row — a value
too long to fit is trimmed rather than wrapped — and a text field sizes itself
to its contents so the popover attaches to what was typed rather than to the
end of an empty box.

Containers are sized to their contents rather than to a fixed width, and
transient notices occupy a permanently reserved line, so nothing on screen
moves when one appears or expires.

Animation is driven by a 60fps frame ticker off the wall clock, independent of
the simulation tick rate — a 10-tick-per-second match shimmers at the same
speed as a 60-tick one, because decoration should not change with the rules.
Snake tails carry a static head-to-tail gradient plus a travelling brightness
wave, so a live snake visibly flows and reads differently from a wall. Food
pulses through four glyphs and a brightness cycle in step. Deaths leave a
briefly fading ember; the countdown is animated; the results dialog slides in.

The arena is rendered into a flat cell buffer and serialised with run-length
colour compression — an escape sequence only where the colour actually changes.
Going through lipgloss per cell would allocate a style and re-measure a string
several thousand times a frame.

**Colour** is authored once in truecolor and downsampled at render time.
`--color` forces a depth; `auto` reads `COLORTERM` and `TERM`. `NO_COLOR`
disables colour entirely and always wins.

**Accessibility** (best-effort, as scoped): every player has a distinct *glyph*
as well as a distinct colour, so seats stay separable with colour off or on a
16-colour terminal — where eight distinct legible hues do not exist, and the
`mono` theme deliberately collapses to greys and leans entirely on shape. All
flows are keyboard-driven with a context-sensitive help bar. `--ascii` swaps in
a pure 7-bit glyph set for terminals that mis-measure geometric shapes. No
emoji anywhere: emoji width is unreliable, and one mis-measured cell shears
every column after it. Bubble Tea offers nothing further for screen readers,
and a TUI of this kind is not usefully screen-reader accessible.

**Layout** degrades rather than corrupting: a window too small for the current
screen gets an overlay stating the required and current size, which itself
scales down to a two-line form and finally to bare dimensions, so it fits any
window it is describing.

**A board bigger than your terminal still plays.** A host can pick an arena
larger than someone else's screen, and that player may have no way to make
their terminal bigger — it may already fill the display. So the arena scrolls:
the view follows your snake, moving only when you approach its edge rather
than sliding under you on every move. The frame is tinted and the header says
`82×18 of 98×40`, because a player who cannot see the whole board should know
that rather than wonder where everyone went. Seeing part of the board is a
real disadvantage; being locked out of a match you have already joined is
worse.

The lobby browser and the lobby room both warn *before* you commit, naming the
size that would show all of it — finding out at kickoff is too late. When a
match starts tailsnail also asks the terminal to grow to fit the whole board
(XTWINOPS `CSI 8 ; rows ; cols t`). Treat that as a bonus: many emulators
never implemented it, most that did ship with it disabled, and a maximised
window has nowhere to grow to. It only ever grows a window, never shrinks one,
and it can be turned off in settings. The genuinely portable answer to a board
that will not fit is to reduce the terminal's font size, which is what the
overlay suggests — and escape leaves the match, which is the one action always
available.

## Testing

```sh
go test ./...
```

- `internal/game` — the simulation is pure and deterministic, with no I/O and
  no dependencies outside the standard library. Movement, turning, self- and
  mutual collision, kill attribution, wrap-around on all four edges, food and
  growth, elimination order and placement ranking, the shrinking arena, and
  seed determinism.
- `internal/proto` — canonicalisation is key-order independent and survives a
  wire round trip; sign/verify; tampering is caught both with and without a
  recomputed hash; signatures from non-participants and under the wrong key are
  rejected; framing, truncation, oversized frames and concurrent senders.
- `internal/store` — persistence, signature merging, refusal to overwrite a
  conflicting record, corrupt-file tolerance, path traversal via match ID, and
  leaderboard aggregation across renames.
- `internal/gossip` — inventory diffing and full three-message exchanges over a
  pipe, including convergence from empty, signature merging, forged records,
  and batch bounding.
- `internal/netplay` — a real host and client over a loopback listener: join,
  ready, countdown, a full match, attestation, and the storage of an identical
  fully-attested record on both peers. Plus kick, host close, lobby-full,
  join-during-match, and duplicate-install refusals.
- `internal/discovery` — probing against synthetic tailnet nodes: filtering,
  backoff, netmap-triggered re-probes, pruning, ordering, and concurrency
  bounds.
- `internal/ui` — every screen rendered across both themes, both glyph sets and
  all four colour depths, asserting no frame exceeds its viewport; that the
  scrolling view keeps the player on screen while never leaving the arena and
  holds still while they are away from its edge; that field
  rows never shift as the selection moves and a notice changes exactly one
  line; that a dialog keeps its size while being scrolled and stops at the
  start of its content; that the countdown never lands on a spawned snake at
  any arena size or seat count; that overlays splice styled and double-width
  content without shearing; and the update path — screen flow
  through a match, stale session generations, join success and failure, and
  settings save versus discard.

TUI behaviour and tsnet integration are exercised manually; there is no
network test harness for Tailscale itself.

## Non-goals

Host migration, spectators, resuming an in-progress match, NAT traversal logic
(tsnet handles it), Windows, public internet play, anti-cheat, and testing
Tailscale itself.

## Layout

```
cmd/tsnail         flags, wiring, shutdown
internal/game      pure deterministic simulation
internal/proto     wire protocol, framing, match records and attestation
internal/store     identity, settings, append-only match log, leaderboard
internal/gossip    anti-entropy
internal/tsnode    embedded tsnet node and the onboarding state machine
internal/discovery netmap polling and handshake probes
internal/netplay   listener, host lobby and game loop, client session
internal/logring   sanitising log ring buffer
internal/ui        Bubble Tea screens
internal/ui/theme  colour model, themes, glyph sets
```

## Licence

MIT.
