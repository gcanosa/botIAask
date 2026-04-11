# TODO: Bot Improvements and Future Features

## Multi-Channel Enhancements
- [ ] Implement channel-specific configurations (e.g., different command prefixes or AI models per channel).
- [ ] Add a way to dynamically join/part channels via IRC commands without restarting the bot.
- [ ] Implement a "blacklist" for specific users or channels.

## Feature Requests
- [ ] **Contextual Memory**: Allow the bot to remember previous messages in a conversation (session-based context).
- [ ] **Image Support**: If the AI model supports it, allow processing of images sent via IRC.
- [ ] **Moderation Tools**: Add commands for banning/kicking users from the bot's "view" or logging bad behavior.
- [ ] **Custom Commands**: Allow users to define their own simple text-replacement commands via IRC.

## Technical Improvements
- [ ] **Logging**: Implement a more structured logging system (e.g., using `slog` or `zap`) with levels and file rotation.
- [ ] **Error Handling**: Improve error recovery for AI API timeouts or connection drops.
- [ ] **Unit Testing**: Increase test coverage for the `irc` and `ai` packages.
- [ ] **Configuration Reloading**: Implement a mechanism to reload `config.yaml` without restarting the process (e.g., using `SIGHUP`).

## Infrastructure & Deployment
- [ ] **Dockerization**: Create a `Dockerfile` and `docker-compose.yml` for easy deployment.
- [ ] **CI/CD**: Set up GitHub Actions to run tests on every push.