import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { ArrowRight, Globe, LucideIcon, Settings, Shield, Sparkles, Zap } from 'lucide-react'
import Link from 'next/link'

interface Feature {
  icon: LucideIcon
  title: string
  description: string
}

const features: Feature[] = [
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

const providers: string[] = [
  'OpenAI',
  'Anthropic',
  'Azure OpenAI',
  'AWS Bedrock',
  'Cohere',
  'Google Vertex AI',
  'Mistral',
  'Ollama'
]

const keyFeatures: string[] = [
  'Load balancing across multiple API keys',
  'Automatic failover and retry logic',
  'Environment variable support',
  'MCP (Model Context Protocol) integration',
  'Configurable timeouts and concurrency',
  'OpenAI-compatible API interface'
]

export default function HomePage() {
  return (
    <div className="min-h-screen bg-background">
      <div className="p-8 max-w-7xl mx-auto">
        <div className="mb-12">
          <div className="flex items-center gap-3 mb-4">
            <div className="flex items-center justify-center w-12 h-12 bg-primary rounded-xl">
              <Sparkles className="h-6 w-6 text-primary-foreground" />
            </div>
            <div>
              <h1 className="text-4xl font-bold mb-2 bg-gradient-to-r from-foreground to-muted-foreground bg-clip-text text-transparent">
                Welcome to Bifrost
              </h1>
              <p className="text-muted-foreground text-lg">
                A high-performance AI provider gateway that intelligently routes requests across multiple AI providers
              </p>
            </div>
          </div>
        </div>

        <div className="grid gap-6 md:grid-cols-2 lg:grid-cols-2 mb-8">
          {features.map((feature) => {
            const Icon = feature.icon
            return (
              <Card key={feature.title} className="group hover:shadow-lg transition-all duration-200 border-border">
                <CardHeader>
                  <div className="flex items-center space-x-3">
                    <div className="flex items-center justify-center w-10 h-10 bg-primary/10 rounded-lg group-hover:bg-primary/20 transition-colors">
                      <Icon className="h-5 w-5 text-primary" />
                    </div>
                    <CardTitle className="text-lg">{feature.title}</CardTitle>
                  </div>
                </CardHeader>
                <CardContent>
                  <CardDescription className="text-sm leading-relaxed">
                    {feature.description}
                  </CardDescription>
                </CardContent>
              </Card>
            )
          })}
        </div>

        <Card className="mb-8 border-border">
          <CardHeader>
            <CardTitle className="flex items-center gap-2">
              <Globe className="h-5 w-5 text-primary" />
              Supported Providers
            </CardTitle>
            <CardDescription>
              Bifrost supports the following AI providers out of the box
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex flex-wrap gap-2">
              {providers.map((provider) => (
                <Badge key={provider} variant="secondary" className="hover:bg-accent transition-colors">
                  {provider}
                </Badge>
              ))}
            </div>
          </CardContent>
        </Card>

        <div className="grid gap-6 md:grid-cols-2">
          <Card className="border-border">
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Settings className="h-5 w-5 text-primary" />
                Getting Started
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="space-y-6">
                <div className="flex gap-4">
                  <div className="flex items-center justify-center w-8 h-8 bg-primary/10 rounded-full text-primary font-semibold text-sm">
                    1
                  </div>
                  <div>
                    <h4 className="font-medium mb-1">Configure Providers</h4>
                    <p className="text-sm text-muted-foreground leading-relaxed">
                      Use the Config tab to set up your AI provider credentials and settings
                    </p>
                  </div>
                </div>
                <div className="flex gap-4">
                  <div className="flex items-center justify-center w-8 h-8 bg-primary/10 rounded-full text-primary font-semibold text-sm">
                    2
                  </div>
                  <div>
                    <h4 className="font-medium mb-1">Start the Server</h4>
                    <p className="text-sm text-muted-foreground leading-relaxed">
                      Launch the Bifrost HTTP server to start routing requests
                    </p>
                  </div>
                </div>
                <div className="flex gap-4">
                  <div className="flex items-center justify-center w-8 h-8 bg-primary/10 rounded-full text-primary font-semibold text-sm">
                    3
                  </div>
                  <div>
                    <h4 className="font-medium mb-1">Make Requests</h4>
                    <p className="text-sm text-muted-foreground leading-relaxed">
                      Send OpenAI-compatible API requests to your Bifrost endpoint
                    </p>
                  </div>
                </div>
                <Button asChild className="w-full mt-6 h-11">
                  <Link href="/config">
                    Configure Now
                    <ArrowRight className="ml-2 h-4 w-4" />
                  </Link>
                </Button>
              </div>
            </CardContent>
          </Card>

          <Card className="border-border">
            <CardHeader>
              <CardTitle className="flex items-center gap-2">
                <Sparkles className="h-5 w-5 text-primary" />
                Key Features
              </CardTitle>
            </CardHeader>
            <CardContent>
              <div className="space-y-4 text-sm">
                {keyFeatures.map((feature, index) => (
                  <div key={index} className="flex items-center space-x-3">
                    <div className="w-1.5 h-1.5 bg-primary rounded-full"></div>
                    <span className="leading-relaxed">{feature}</span>
                  </div>
                ))}
              </div>
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  )
} 