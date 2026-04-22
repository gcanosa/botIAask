# botIAask

**botIAask** is a feature-rich IRC bot powered by AI, designed for modern channel management and interactive experiences. Built in Go, it combines traditional IRC bot functionalities with advanced AI integration (via LM Studio), a professional web dashboard, and a secure administrative workflow.

## 🚀 Key Features

### 🤖 AI & IRC Interaction
- **AI-Powered Responses**: Ask questions directly to an AI model using `!ask`.
- **RSS & news**: Configurable feeds; optional IRC announcements and per-channel toggles.
- **Rate Limiting**: Intelligent command throttling to prevent spam.
- **Log Rotation**: Automatic multi-day log management for all channels.

### 📊 Web Dashboard
- **Real-time Monitoring**: Track uptime, AI requests, and connection status.
- **Live Log Streaming**: View IRC channel activity directly in the browser via SSE.
- **interactive Stats**: Visualized bot usage and channel metrics with historical views.
- **Link Bookmarks**: Search and manage bookmarked URLs from IRC.
- **Financial Dashboard**: Real-time tracking of Crypto, Euro, and Argentinean Peso rates.

### 📝 Secure Paste System
- **Ephemeral Uploads**: Users can generate temporary links with `!paste` to upload logs or code.
- **File uploads**: `!upload` provides a time-limited form to submit a binary file (size limit configurable in the web dashboard). The IRC bot and web dashboard must use the **same** `uploads` SQLite file (see `uploads.db_path` in config if your working directory differs between processes).
- **Admin Approval**: Pastes and file uploads require manual approval before public links work.
- **Syntax Highlighting**: Beautiful rendering of shared code snippets.
- **Auto-Expiration**: Pastes are automatically removed after a configurable duration.

---

## 🛠 Quick Setup

### Prerequisites
- [Go](https://golang.org/doc/install) 1.22 or higher (see `go.mod` for the exact toolchain the project targets).
- [LM Studio](https://lmstudio.ai/) (for AI capabilities) or any OpenAI-compatible API.

### Installation

1. **Clone the repository:**
   ```bash
   git clone https://github.com/gcanosa/botIAask.git
   cd botIAask
   ```

2. **Configure the bot:**
   Copy `config/config.yaml.template` to `config/config.yaml` and edit `config/config.yaml` to set your IRC server, channel preferences, AI endpoint, and all internal parameters.
   ```yaml
   irc:
     server: "irc.libera.chat"
     nickname: "MyBot"
     channels: ["#mychannel"]
   ai:
     lm_studio_url: "http://localhost:1234/v1"
   ```

3. **Run the bot:**
   ```bash
   # Remember to copy config.yaml.template file from config folder to a file named config.yaml and set up the parameters, otherwise bot will fail to load configuration.

   # Run in foreground for debugging
   go run main.go

   # Or run with the web dashboard enabled in daemon mode
   go run main.go -dashboard
   ```

---

## 💻 Command Line Interface

`botIAask` includes a CLI to manage the bot’s lifecycle, optional RSS maintenance, and quick introspection.

- **`-h` / `-help`**: prints application flags only (name, version, and `flag` defaults). It does not list IRC commands.
- **`-usage`**: prints a full **IRC** command reference, grouped into user commands and admin commands. On a color-capable TTY, user commands and admin commands use different colors; set the environment variable **`NO_COLOR`** (to any value) to force plain text.

| Option | Description |
| :--- | :--- |
| `-dashboard` | Starts the bot in daemon mode and enables the web dashboard. |
| `-daemon` | Runs the bot in the background (daemon mode). |
| `-debug` | Verbose console output from the process (default: `true`). |
| `-mode <start\|stop\|restart>` | Controls the daemon process explicitly. |
| `-news` | Enables the RSS fetcher in the background. |
| `-updatenews [limit]` | Backfills the news database and exits. |
| `-dropnews` | Clears all news data and exits. |
| `-rehash` | Sends `SIGHUP` to the PID in `daemon.pid` so a running daemon reloads `config/config.yaml`. |
| `-usage` | Prints the IRC user/admin command list (see above). |
| `-version` | Prints name and version. |
| `-about` | Shows project details and author. |

In **`config/config.yaml`**, under `rss`, **`announce_to_irc`** defaults to `true`. Set it to `false` to keep fetching and updating the local news database without posting to IRC (avoids flooding the channel after long downtime). You can also run **`-updatenews [limit]`** once to backfill the database before starting the bot.

---

## 💬 IRC Commands

### User Commands
Command **prefix** (default `!`) and the AI trigger name (default `ask`) are set in `config/config.yaml` under `bot.command_prefix` and `bot.command_name`.

| Command | Description |
| :--- | :--- |
| `!ask <query>` | Asks the AI; same behavior as the configured command name. |
| `!bc <expr>` | Evaluates a math expression (e.g. `5+5`). |
| `!news [limit]` | Fetches recent items from the configured RSS database in channels where news is enabled (limit 1–10). |
| `!bookmark` | `ADD <URL> [nickname]` or `FIND <text>` to add or search bookmarks. |
| `!uptime` | Application uptime and current IRC session uptime. |
| `!spec` | Shows the system prompt spec string used for AI replies. |
| `!paste` | Generates a secure, temporary link to upload a text paste. |
| `!upload` | Generates a secure, temporary link to upload a file (max size in web **Uploads** settings). |
| `!download [N]` | Lists your approved uploads with download URLs (newest first; optional last *N*). |
| `!euro` | Euro / forex panel. |
| `!peso` | Argentine peso view. |
| `!crypto` | Crypto market view. |
| `!reminder` | `add <note>`, `del <id>`, or `list` (per-user reminders). |
| `!help` | Short in-channel summary of commands. |

### Admin Commands
*Admins must match a **hostmask** in `config/config.yaml` **and** be in an active **`!admin`** session.* Mode commands (`!op`, `!deop`, `!voice`, `!devoice`) are used **in a channel** where the bot can set modes.

| Command | Description |
| :--- | :--- |
| `!admin` | Starts an admin session. |
| `!admin off` | Ends the admin session. |
| `!join #channel [key]` | Joins a channel; optional channel key is stored in config. |
| `!part [#channel]` | Parts a channel and updates `config/config.yaml` when applicable. |
| `!ignore <nick>` | Ignores a nickname. |
| `!say #channel <message>` | Sends a message to a channel. |
| `!news on` / `!news off` | Enables or disables news for the **current** channel (session only). |
| `!news start` / `!news stop` | Turns global RSS-to-IRC announcements on or off; persists `rss.announce_to_irc` in config. |
| `!stats` | Bot statistics (e.g. AI request count, uptime). |
| `!op [nick]` / `!deop [nick]` | Channel operator; in a channel, acts on the invoker or the given nick. |
| `!voice [nick]` / `!devoice [nick]` | Channel voice. |
| `!ticket pending` | Lists pending paste/file tickets. |
| `!ticket approve <ID>` | Approves a ticket (paste or file). |
| `!ticket cancel <ID>` | Cancels a ticket. |
| `!rehash` | Reloads configuration from disk (notifies other admins). |
| `!quit [reason]` | Disconnects; quit message from `irc.quit_message` or default banner. |

---

## 🌐 Web Dashboard Features

The dashboard (default: `http://localhost:3366`) provides several administrative and public features:

- **Admin Control Panel**: Manage pastes, toggle bot features, and manage web user accounts.
- **Live Logs**: Dedicated view for watching channel activity in real-time.
- **Paste Viewer**: Publicly accessible view for approved pastes (`/p/<ID>`).
- **File uploads**: Admin **Uploads** panel, configurable max size; approved files at `/f/<ID>`.
- **Market View**: Live financial data panel for crypto and currency rates.
- **System Stats**: Detailed charts for AI requests and system performance.
- **Bookmarks**: Searchable database of links shared across channels.

---

## 📄 License

This project is maintained by **Gerardo Canosa** (gera.canosa@gmail.com). 
Check the `LICENSE` file for more details.
