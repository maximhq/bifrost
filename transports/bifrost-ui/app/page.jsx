import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ArrowRight, Globe, Settings, Shield, Zap } from 'lucide-react'
import Link from 'next/link'

const features = [
  {
    icon: Zap,
    title: 'High Performance',
    description: 'Optimized for low latency and high throughput AI provider routing'
  },
  {
    icon: Shield,
    title: 'Secure',
    description: 'Built-in security features with proper API key management'
  },
  {
    icon: Globe,
    title: 'Multi-Provider',
    description: 'Support for OpenAI, Anthropic, Azure, Bedrock, Cohere, and more'
  },
  {
    icon: Settings,
    title: 'Configurable',
    description: 'Flexible configuration with load balancing and failover support'
  }
]

const providers = [
  'OpenAI',
  'Anthropic',
  'Azure OpenAI',
  'AWS Bedrock',
  'Cohere',
  'Google Vertex AI',
  'Mistral',
  'Ollama'
]

export default function HomePage () {
  return (
    <div className="p-8">
      <div className="mb-8">
        <h1 className="text-3xl font-bold mb-2">Welcome to Bifrost</h1>
        <p className="text-muted-foreground text-lg">
          A high-performance AI provider gateway that routes requests across multiple AI providers
        </p>
      </div>

      <div className="grid gap-6 md:grid-cols-2 lg:grid-cols-2 mb-8">
        {features.map((feature) => {
          const Icon = feature.icon
          return (
            <Card key={feature.title}>
              <CardHeader>
                <div className="flex items-center space-x-2">
                  <Icon className="h-5 w-5 text-primary" />
                  <CardTitle className="text-lg">{feature.title}</CardTitle>
                </div>
              </CardHeader>
              <CardContent>
                <CardDescription className="text-sm">
                  {feature.description}
                </CardDescription>
              </CardContent>
            </Card>
          )
        })}
      </div>

      <Card className="mb-8">
        <CardHeader>
          <CardTitle>Supported Providers</CardTitle>
          <CardDescription>
            Bifrost supports the following AI providers out of the box
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="flex flex-wrap gap-2">
            {providers.map((provider) => (
              <Badge key={provider} variant="secondary">
                {provider}
              </Badge>
            ))}
          </div>
        </CardContent>
      </Card>

      <div className="grid gap-6 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle>Getting Started</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <div>
                <h4 className="font-medium mb-2">1. Configure Providers</h4>
                <p className="text-sm text-muted-foreground">
                  Use the Config tab to set up your AI provider credentials and settings
                </p>
              </div>
              <div>
                <h4 className="font-medium mb-2">2. Start the Server</h4>
                <p className="text-sm text-muted-foreground">
                  Launch the Bifrost HTTP server to start routing requests
                </p>
              </div>
              <div>
                <h4 className="font-medium mb-2">3. Make Requests</h4>
                <p className="text-sm text-muted-foreground">
                  Send OpenAI-compatible API requests to your Bifrost endpoint
                </p>
              </div>
              <Button asChild className="w-full mt-4">
                <Link href="/config">
                  Configure Now
                  <ArrowRight className="ml-2 h-4 w-4" />
                </Link>
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Key Features</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-3 text-sm">
              <div className="flex items-center space-x-2">
                <div className="w-2 h-2 bg-primary rounded-full"></div>
                <span>Load balancing across multiple API keys</span>
              </div>
              <div className="flex items-center space-x-2">
                <div className="w-2 h-2 bg-primary rounded-full"></div>
                <span>Automatic failover and retry logic</span>
              </div>
              <div className="flex items-center space-x-2">
                <div className="w-2 h-2 bg-primary rounded-full"></div>
                <span>Environment variable support</span>
              </div>
              <div className="flex items-center space-x-2">
                <div className="w-2 h-2 bg-primary rounded-full"></div>
                <span>MCP (Model Context Protocol) integration</span>
              </div>
              <div className="flex items-center space-x-2">
                <div className="w-2 h-2 bg-primary rounded-full"></div>
                <span>Configurable timeouts and concurrency</span>
              </div>
              <div className="flex items-center space-x-2">
                <div className="w-2 h-2 bg-primary rounded-full"></div>
                <span>OpenAI-compatible API interface</span>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  )
} 