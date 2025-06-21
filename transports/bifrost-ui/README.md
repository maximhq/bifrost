# Bifrost UI

A modern web interface for configuring and managing the Bifrost AI provider gateway.

## Features

- **Modern UI**: Built with Next.js 15, React 19, and Tailwind CSS
- **Static Export**: Generates static HTML/JS that can be served by any web server
- **Configuration Management**: JSON-based configuration editor with validation
- **Responsive Design**: Mobile-first design using Shadcn UI components
- **Provider Support**: Configure multiple AI providers (OpenAI, Anthropic, Azure, etc.)

## Getting Started

### Prerequisites

- Node.js 18.17 or later
- npm or yarn

### Installation

1. Install dependencies:
```bash
npm install
```

2. Run the development server:
```bash
npm run dev
```

3. Open [http://localhost:3000](http://localhost:3000) in your browser.

### Building for Production

To create a static export that can be served by the Bifrost HTTP server:

```bash
npm run build
```

This will generate static files in the `out` directory.

## Configuration

The UI provides a JSON editor for managing Bifrost configuration. The configuration includes:

- **Providers**: AI provider settings (OpenAI, Anthropic, Azure, etc.)
- **API Keys**: Environment variable references for secure key management
- **Network Config**: Timeout, retry, and connection settings
- **MCP Integration**: Model Context Protocol client configurations

## Project Structure

```
bifrost-ui/
├── app/                 # Next.js App Router pages
│   ├── config/         # Configuration page
│   ├── globals.css     # Global styles
│   ├── layout.jsx      # Root layout
│   └── page.jsx        # Home page
├── components/         # React components
│   ├── ui/            # Shadcn UI components
│   └── sidebar.jsx    # Navigation sidebar
├── lib/               # Utility functions
└── public/            # Static assets
```

## Tech Stack

- **Framework**: Next.js 15 with App Router
- **React**: React 19
- **Styling**: Tailwind CSS + Shadcn UI
- **Icons**: Lucide React
- **Export**: Static HTML/JS generation

## Development

- `npm run dev` - Start development server
- `npm run build` - Build for production
- `npm run start` - Start production server
- `npm run lint` - Run ESLint

## Integration with Bifrost HTTP

The static build output can be served by the Bifrost HTTP transport server. Place the contents of the `out` directory in your web server's static file directory. 