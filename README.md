# botIAask

**botIAask** is a feature-rich IRC bot powered by AI, designed for modern channel management and interactive experiences. Built in Go, it combines traditional IRC bot functionalities with advanced AI integration (via LM Studio), a professional web dashboard, and a secure administrative workflow.

## 🚀 Key Features

### 🤖 AI & IRC Interaction
- **AI-Powered Responses**: Ask questions directly to an AI model using `!ask`.
- **Hacker News Integration**: Automatically fetch and broadcast top stories.
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
- [Go](https://golang.org/doc/install) 1.21 or higher.
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

`botIAask` includes a powerful CLI to manage the bot's lifecycle and background processes.

| Option | Description |
| :--- | :--- |
| `-dashboard` | Starts the bot in daemon mode and enables the web dashboard. |
| `-daemon` | Runs the bot in background (daemon mode). |
| `-debug` | Enables verbose debug output in the console. |
| `-mode <start\|stop\|restart>` | Controls the daemon process explicitly. |
| `-news` | Enables the RSS fetcher in the background. |
| `-updatenews [limit]` | Backfills the news database and exits. |
| `-dropnews` | Clears all news data and exits. |
| `-version` | Displays current version information. |
| `-about` | Shows project details and developer info. |

In **`config/config.yaml`**, under `rss`, **`announce_to_irc`** defaults to `true`. Set it to `false` to keep fetching and updating the local news database without posting to IRC (avoids flooding the channel after long downtime). You can also run **`-updatenews [limit]`** once to backfill the database before starting the bot.

---

## 💬 IRC Commands

### User Commands
| Command | Description |
| :--- | :--- |
| `!ask <query>` | Asks the AI a question and returns the response. |
| `!news [limit]` | Fetches the latest stories from Hacker News. |
| `!paste` | Generates a secure, temporary link to upload a text paste. |
| `!upload` | Generates a secure, temporary link to upload a file (max size in web **Uploads** settings). |
| `!uptime` | Displays how long the bot has been running. |
| `!spec` | Shows the current AI system prompt specification. |
| `!help` | Displays help information in the channel. |

### Admin Commands
*Admins must match hostmask in config AND be in an active `!admin` session.*

| Command | Description |
| :--- | :--- |
| `!admin` | Logs into an administrative session. |
| `!admin off` | Ends the administrative session. |
| `!ticket approve <ID>` | Approves a pending paste for public viewing. |
| `!ticket cancel <ID>` | Rejects and deletes a pending paste. |
| `!join #channel` | Commands the bot to join a specific channel. |
| `!part [#channel]` | Commands the bot to leave a channel. |
| `!stats` | Displays internal bot statistics. |
| `!news on/off` | Toggles automatic news broadcasts in the current channel (session only). |
| `!news start` / `!news stop` | Turns global RSS-to-IRC announcements on or off, saves `rss.announce_to_irc` in config, and applies immediately (in-flight fetch stops posting when stopped). Requires admin hostmask and `!admin` session. |
| `!quit [reason]` | Shuts down the bot safely. |

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
