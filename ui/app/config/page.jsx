'use client'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Textarea } from '@/components/ui/textarea'
import { AlertCircle, CheckCircle, Download, Save, Upload } from 'lucide-react'
import { useState } from 'react'

const defaultConfig = {
  providers: {
    openai: {
      keys: [
        {
          value: 'env.OPENAI_API_KEY',
          models: ['gpt-3.5-turbo', 'gpt-4', 'gpt-4o', 'gpt-4o-mini'],
          weight: 1.0
        }
      ],
      network_config: {
        default_request_timeout_in_seconds: 30,
        max_retries: 1,
        retry_backoff_initial_ms: 100,
        retry_backoff_max_ms: 2000
      },
      concurrency_and_buffer_size: {
        concurrency: 3,
        buffer_size: 10
      }
    },
    anthropic: {
      keys: [
        {
          value: 'env.ANTHROPIC_API_KEY',
          models: ['claude-3-5-sonnet-20240620', 'claude-3-haiku-20240307'],
          weight: 1.0
        }
      ],
      network_config: {
        default_request_timeout_in_seconds: 30,
        max_retries: 1,
        retry_backoff_initial_ms: 100,
        retry_backoff_max_ms: 2000
      },
      concurrency_and_buffer_size: {
        concurrency: 3,
        buffer_size: 10
      }
    }
  },
  mcp: {
    client_configs: []
  }
}

export default function ConfigPage () {
  const [config, setConfig] = useState(JSON.stringify(defaultConfig, null, 2))
  const [isValid, setIsValid] = useState(true)
  const [error, setError] = useState('')
  const [saveStatus, setSaveStatus] = useState('')

  const validateConfig = (configString) => {
    try {
      JSON.parse(configString)
      setIsValid(true)
      setError('')
      return true
    } catch (err) {
      setIsValid(false)
      setError(err.message)
      return false
    }
  }

  const handleConfigChange = (value) => {
    setConfig(value)
    validateConfig(value)
  }

  const handleSave = () => {
    if (validateConfig(config)) {
      // In a real implementation, this would save to a backend
      setSaveStatus('Configuration saved successfully!')
      setTimeout(() => setSaveStatus(''), 3000)
    }
  }

  const handleDownload = () => {
    if (validateConfig(config)) {
      const blob = new Blob([config], { type: 'application/json' })
      const url = URL.createObjectURL(blob)
      const a = document.createElement('a')
      a.href = url
      a.download = 'bifrost-config.json'
      document.body.appendChild(a)
      a.click()
      document.body.removeChild(a)
      URL.revokeObjectURL(url)
    }
  }

  const handleFileUpload = (event) => {
    const file = event.target.files[0]
    if (file) {
      const reader = new FileReader()
      reader.onload = (e) => {
        const content = e.target.result
        setConfig(content)
        validateConfig(content)
      }
      reader.readAsText(file)
    }
  }

  const handleReset = () => {
    setConfig(JSON.stringify(defaultConfig, null, 2))
    setIsValid(true)
    setError('')
  }

  return (
    <div className="p-8">
      <div className="mb-8">
        <h1 className="text-3xl font-bold mb-2">Configuration</h1>
        <p className="text-muted-foreground">
          Configure your AI providers, API keys, and settings
        </p>
      </div>

      <div className="grid gap-6">
        <Card>
          <CardHeader>
            <div className="flex items-center justify-between">
              <div>
                <CardTitle>JSON Configuration</CardTitle>
                <CardDescription>
                  Edit your Bifrost configuration in JSON format
                </CardDescription>
              </div>
              <div className="flex items-center space-x-2">
                {isValid ? (
                  <Badge variant="secondary" className="flex items-center space-x-1">
                    <CheckCircle className="h-3 w-3" />
                    <span>Valid</span>
                  </Badge>
                ) : (
                  <Badge variant="destructive" className="flex items-center space-x-1">
                    <AlertCircle className="h-3 w-3" />
                    <span>Invalid</span>
                  </Badge>
                )}
              </div>
            </div>
          </CardHeader>
          <CardContent>
            <div className="space-y-4">
              <Textarea
                value={config}
                onChange={(e) => handleConfigChange(e.target.value)}
                className="min-h-[400px] font-mono text-sm"
                placeholder="Enter your configuration JSON here..."
              />
              {!isValid && error && (
                <div className="text-sm text-destructive bg-destructive/10 p-3 rounded-md">
                  <strong>JSON Error:</strong> {error}
                </div>
              )}
              {saveStatus && (
                <div className="text-sm text-green-600 bg-green-50 p-3 rounded-md">
                  {saveStatus}
                </div>
              )}
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Actions</CardTitle>
            <CardDescription>
              Save, download, or upload your configuration
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="flex flex-wrap gap-3">
              <Button onClick={handleSave} disabled={!isValid}>
                <Save className="mr-2 h-4 w-4" />
                Save Configuration
              </Button>
              <Button variant="outline" onClick={handleDownload} disabled={!isValid}>
                <Download className="mr-2 h-4 w-4" />
                Download JSON
              </Button>
              <Button variant="outline" asChild>
                <label className="cursor-pointer">
                  <Upload className="mr-2 h-4 w-4" />
                  Upload JSON
                  <input
                    type="file"
                    accept=".json"
                    onChange={handleFileUpload}
                    className="hidden"
                  />
                </label>
              </Button>
              <Button variant="outline" onClick={handleReset}>
                Reset to Default
              </Button>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle>Configuration Guide</CardTitle>
          </CardHeader>
          <CardContent>
            <div className="space-y-4 text-sm">
              <div>
                <h4 className="font-medium mb-2">Providers</h4>
                <p className="text-muted-foreground">
                  Configure AI providers like OpenAI, Anthropic, Azure, etc. Each provider needs API keys and model specifications.
                </p>
              </div>
              <div>
                <h4 className="font-medium mb-2">Environment Variables</h4>
                <p className="text-muted-foreground">
                  Use "env.VARIABLE_NAME" format to reference environment variables for sensitive data like API keys.
                </p>
              </div>
              <div>
                <h4 className="font-medium mb-2">Load Balancing</h4>
                <p className="text-muted-foreground">
                  Set weights for different API keys to control load distribution across multiple keys.
                </p>
              </div>
              <div>
                <h4 className="font-medium mb-2">MCP Integration</h4>
                <p className="text-muted-foreground">
                  Configure Model Context Protocol (MCP) clients for extended functionality.
                </p>
              </div>
            </div>
          </CardContent>
        </Card>
      </div>
    </div>
  )
} 