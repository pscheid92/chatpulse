# Twitch Sentiment Overlay

A real-time sentiment tracking overlay for Twitch streamers. Monitor your chat's sentiment during polls, debates, or Q&A sessions with a beautiful glassmorphism overlay for OBS.

## Features

- **Real-time sentiment tracking** from Twitch chat messages
- **Customizable triggers** for "for" and "against" votes
- **Beautiful glassmorphism overlay** for OBS
- **Configurable decay speed** to smoothly center the sentiment
- **Automatic reconnection** handling for both EventSub and overlay clients
- **Single binary deployment** with Docker support

## Prerequisites

1. **Twitch Application**: Register your app at https://dev.twitch.tv/console/apps
   - Get your Client ID and Client Secret
   - Set the OAuth Redirect URL to `http://localhost:8080/auth/callback` (or your domain)

2. **PostgreSQL**: Version 15 or higher

3. **Go**: Version 1.22 or higher (for local development)

## Quick Start with Docker

1. Clone the repository:
```bash
git clone https://github.com/pscheid92/twitch-tow.git
cd twitch-tow
```

2. Create a `.env` file from the example:
```bash
cp .env.example .env
```

3. Edit `.env` and fill in your Twitch credentials:
```bash
TWITCH_CLIENT_ID=your_client_id_here
TWITCH_CLIENT_SECRET=your_client_secret_here
TWITCH_REDIRECT_URI=http://localhost:8080/auth/callback
SESSION_SECRET=$(openssl rand -hex 32)
```

4. Start the application:
```bash
docker compose up -d
```

5. Open your browser and navigate to `http://localhost:8080`

## Local Development

1. Install dependencies:
```bash
go mod download
```

2. Start PostgreSQL (or use Docker):
```bash
docker run -d \
  --name twitch-postgres \
  -e POSTGRES_USER=twitchuser \
  -e POSTGRES_PASSWORD=twitchpass \
  -e POSTGRES_DB=twitchdb \
  -p 5432:5432 \
  postgres:15-alpine
```

3. Set up your `.env` file as described above.

4. Run the server:
```bash
go run cmd/server/main.go
```

## Usage

### 1. Configure Your Overlay

1. Visit `http://localhost:8080` and log in with your Twitch account
2. Configure your sentiment triggers:
   - **For Trigger**: The word/phrase viewers type to vote "for" (e.g., "yes", "agree", "👍")
   - **Against Trigger**: The word/phrase viewers type to vote "against" (e.g., "no", "disagree", "👎")
   - **Left Label**: Display label for "against" side (e.g., "Against", "No")
   - **Right Label**: Display label for "for" side (e.g., "For", "Yes")
   - **Decay Speed**: How quickly the bar returns to center (0.1 = slow, 2.0 = fast)

3. Click "Save Configuration"

### 2. Add to OBS

1. Copy your unique overlay URL from the dashboard
2. In OBS, add a new **Browser Source**
3. Paste your overlay URL
4. Set dimensions: 800x100 (or adjust to your preference)
5. Done! The overlay will show live sentiment from your chat

### 3. Reset During Stream

Use the "Reset to Center" button on the dashboard to reset the sentiment bar to the center position during your stream.

## How It Works

- **Chat Integration**: Connects to Twitch EventSub WebSocket to receive real-time chat messages
- **Vote Processing**: Messages containing trigger words are counted as votes (case-insensitive substring match)
- **Debouncing**: Each viewer can vote once per second to prevent spam
- **Decay**: The sentiment bar gradually returns to center based on the configured decay speed
- **Real-time Updates**: Overlay updates 20 times per second (50ms ticker) for smooth animation

## Architecture

- **Backend**: Single Go binary serving HTTP, WebSocket, and managing Twitch EventSub
- **Database**: PostgreSQL for user data and configuration
- **Frontend**: Minimal HTML/CSS/JS with no external dependencies
- **Deployment**: Docker Compose with multi-stage build for minimal image size

## Troubleshooting

### Overlay not connecting?
- Check that your server is running and accessible
- Verify the overlay URL is correct
- Check browser console for WebSocket errors

### Chat messages not being tracked?
- Ensure you've logged in with your Twitch account
- Verify your Twitch app has the correct OAuth redirect URI
- Check server logs for EventSub connection status

### EventSub reconnection issues?
- The server automatically handles graceful and ungraceful reconnects
- Check logs for reconnection attempts
- Verify your internet connection is stable

## Production Deployment

For production, make sure to:

1. Use HTTPS and WSS (secure WebSocket)
2. Update `TWITCH_REDIRECT_URI` to your production domain
3. Use a strong `SESSION_SECRET` (generate with `openssl rand -hex 32`)
4. Set `APP_ENV=production`
5. Configure proper PostgreSQL backups
6. Use a reverse proxy (nginx/Caddy) for SSL termination

## License

MIT License - see LICENSE file for details

## Contributing

Contributions are welcome! Please open an issue or submit a pull request.

## Support

For issues and questions, please open an issue on GitHub: https://github.com/pscheid92/twitch-tow/issues
