'use client'

import { Badge } from '@/components/ui/badge'
import { Sheet, SheetContent, SheetHeader, SheetTitle } from '@/components/ui/sheet'
import { LogEntry } from '@/lib/types/logs'
import { DollarSign, FileText, Info, Timer } from 'lucide-react'
import LogEntryDetailsView from './ui/log-entry-details-view'
import moment from 'moment'
import { DottedSeparator } from '@/components/ui/separator'
import { PROVIDER_LABELS, Provider, Status, STATUS_COLORS, REQUEST_TYPE_LABELS, REQUEST_TYPE_COLORS } from '@/lib/constants/logs'
import { CodeEditor } from './ui/code-editor'
import LogMessageView from './ui/log-message-view'
import { Tooltip, TooltipContent, TooltipProvider, TooltipTrigger } from '@/components/ui/tooltip'
import SpeechView from './ui/speech-view'
import TranscriptionView from './ui/transcription-view'
import { renderProviderIcon, ProviderIconType } from '@/lib/constants/icons'

interface LogDetailSheetProps {
  log: LogEntry | null
  open: boolean
  onOpenChange: (open: boolean) => void
}

export function LogDetailSheet({ log, open, onOpenChange }: LogDetailSheetProps) {
  if (!log) return null

  // Performance scoring with muted colors
  const getLatencyScore = (latency: number) => {
    if (latency < 500) return { score: 95, label: 'Excellent' }
    if (latency < 1000) return { score: 80, label: 'Good' }
    if (latency < 2000) return { score: 60, label: 'Fair' }
    return { score: 30, label: 'Poor' }
  }

  const latencyScore = getLatencyScore(log.latency || 0)
  const tokenUsage = log.token_usage
  const tokenEfficiency = tokenUsage ? Math.round((tokenUsage.completion_tokens / tokenUsage.total_tokens) * 100) : 0

  // Calculate estimated costs (example rates)
  const estimatedCost = tokenUsage
    ? {
        inputCost: (tokenUsage.prompt_tokens / 1000) * 0.01, // $0.01 per 1K tokens
        outputCost: (tokenUsage.completion_tokens / 1000) * 0.03, // $0.03 per 1K tokens
        total: (tokenUsage.prompt_tokens / 1000) * 0.01 + (tokenUsage.completion_tokens / 1000) * 0.03,
      }
    : { inputCost: 0, outputCost: 0, total: 0 }

  // Taking out tool call
  let toolsParameter = null
  if (log.params?.tools) {
    try {
      toolsParameter = JSON.stringify(log.params.tools, null, 2)
    } catch (ignored) {}
  }

  let toolChoice = null
  if (log.params?.tool_choice) {
    try {
      toolChoice = JSON.stringify(log.params.tool_choice, null, 2)
    } catch (ignored) {}
  }

  return (
    <Sheet open={open} onOpenChange={onOpenChange}>
      <SheetContent className="flex w-full flex-col overflow-x-hidden p-8 sm:max-w-2xl">
        <SheetHeader className="px-0">
          <SheetTitle className="flex w-fit items-center gap-2 font-medium">
            {log.status === 'success' && <p className="text-md max-w-full truncate">Request ID: {log.id}</p>}
            <Badge variant="outline" className={STATUS_COLORS[log.status as Status]}>
              {log.status}
            </Badge>
          </SheetTitle>
        </SheetHeader>
        <div className="space-y-4 rounded-sm border px-6 py-4">
          <div className="space-y-4">
            <BlockHeader title="Timings" icon={<Timer className="h-5 w-5 text-gray-600" />} />
            <div className="grid w-full grid-cols-3 items-center justify-between gap-4">
              <LogEntryDetailsView
                className="w-full"
                label="Start Timestamp"
                value={moment(log.timestamp).format('YYYY-MM-DD HH:mm:ss A')}
              />
              <LogEntryDetailsView
                className="w-full"
                label="End Timestamp"
                value={moment(log.timestamp)
                  .add(log.latency || 0, 'ms')
                  .format('YYYY-MM-DD HH:mm:ss A')}
              />
              <LogEntryDetailsView
                className="w-full"
                label="Latency"
                value={isNaN(log.latency || 0) ? 'NA' : <div>{(log.latency || 0)?.toFixed(2)}ms</div>}
              />
            </div>
          </div>
          <DottedSeparator />
          <div className="space-y-4">
            <BlockHeader title="Request Details" icon={<FileText className="h-5 w-5 text-gray-600" />} />
            <div className="grid w-full grid-cols-3 items-center justify-between gap-4">
              <LogEntryDetailsView
                className="w-full"
                label="Provider"
                value={
                  <Badge variant="secondary" className={`uppercase`}>
                    {renderProviderIcon(log.provider as ProviderIconType, { size: 'sm' })}
                    {log.provider}
                  </Badge>
                }
              />
              <LogEntryDetailsView className="w-full" label="Model" value={log.model} />
              <LogEntryDetailsView
                className="w-full"
                label="Type"
                value={
                  <div className={`${REQUEST_TYPE_COLORS[log.object as keyof typeof REQUEST_TYPE_COLORS]} rounded-md px-3 py-1`}>
                    {REQUEST_TYPE_LABELS[log.object as keyof typeof REQUEST_TYPE_LABELS]}
                  </div>
                }
              />

              {log.params &&
                Object.keys(log.params).length > 0 &&
                Object.entries(log.params)
                  .filter(([key]) => key !== 'tools')
                  .filter(([_, value]) => typeof value === 'boolean' || typeof value === 'number' || typeof value === 'string')
                  .map(([key, value]) => <LogEntryDetailsView key={key} className="w-full" label={key} value={value} />)}
            </div>
          </div>
          {log.status === 'success' && (
            <>
              <DottedSeparator />
              <div className="space-y-4">
                <BlockHeader title="Tokens" icon={<DollarSign className="h-5 w-5 text-gray-600" />} />
                <div className="grid w-full grid-cols-3 items-center justify-between gap-4">
                  <LogEntryDetailsView className="w-full" label="Prompt Tokens" value={log.token_usage?.prompt_tokens || '-'} />
                  <LogEntryDetailsView className="w-full" label="Completion Tokens" value={log.token_usage?.completion_tokens || '-'} />
                  <LogEntryDetailsView className="w-full" label="Total Tokens" value={log.token_usage?.total_tokens || '-'} />
                </div>
              </div>
              {/* <DottedSeparator />
							<div className="space-y-4">
								<BlockHeader title="Cost" icon={<DollarSign className="h-5 w-5 text-gray-600" />} />
								<div className="grid w-full grid-cols-3 items-center justify-between gap-4">
									<LogEntryDetailsView className="w-full" label="Input Cost" value={`$${(estimatedCost.inputCost / 100).toFixed(4)}`} />
									<LogEntryDetailsView className="w-full" label="Output Cost" value={`$${(estimatedCost.outputCost / 100).toFixed(4)}`} />
									<LogEntryDetailsView className="w-full" label="Total Cost" value={`$${(estimatedCost.total / 100).toFixed(4)}`} />
								</div>
							</div> */}
            </>
          )}
        </div>
        {toolChoice && (
          <div className="w-full rounded-sm border">
            <div className="border-b px-6 py-2 text-sm font-medium">Tool Choice</div>
            <CodeEditor
              className="z-0 w-full"
              shouldAdjustInitialHeight={true}
              maxHeight={450}
              wrap={true}
              code={toolChoice}
              lang="json"
              readonly={true}
              options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: 'off', alwaysConsumeMouseWheel: false }}
            />
          </div>
        )}
        {toolsParameter && (
          <div className="w-full rounded-sm border">
            <div className="border-b px-6 py-2 text-sm font-medium">Tools</div>
            <CodeEditor
              className="z-0 w-full"
              shouldAdjustInitialHeight={true}
              maxHeight={450}
              wrap={true}
              code={toolsParameter}
              lang="json"
              readonly={true}
              options={{ scrollBeyondLastLine: false, collapsibleBlocks: true, lineNumbers: 'off', alwaysConsumeMouseWheel: false }}
            />
          </div>
        )}

        {/* Speech and Transcription Views */}
        {(log.speech_input || log.speech_output) && (
          <>
            <div className="mt-4 w-full text-center text-sm font-medium">Speech</div>
            <SpeechView speechInput={log.speech_input} speechOutput={log.speech_output} isStreaming={log.stream} />
          </>
        )}

        {(log.transcription_input || log.transcription_output) && (
          <>
            <div className="mt-4 w-full text-center text-sm font-medium">Transcription</div>
            <TranscriptionView
              transcriptionInput={log.transcription_input}
              transcriptionOutput={log.transcription_output}
              isStreaming={log.stream}
            />
          </>
        )}

        {/* Show conversation history for chat/text completions */}
        {log.input_history && log.input_history.length > 0 && (
          <>
            <div className="mt-4 w-full text-center text-sm font-medium">Conversation History</div>
            {log.input_history.map((message, index) => (
              <LogMessageView key={index} message={message} />
            ))}
          </>
        )}

        {log.status !== 'processing' && (
          <>
            {log.output_message && (
              <>
                <div className="mt-4 flex w-full items-center justify-center gap-2">
                  <div className="text-sm font-medium">Response</div>
                  {log.stream && (
                    <TooltipProvider>
                      <Tooltip>
                        <TooltipTrigger asChild>
                          <Info className="h-4 w-4 text-gray-600" />
                        </TooltipTrigger>
                        <TooltipContent>
                          The response shown may appear incomplete or out of order due to the way streamed data is accumulated for real-time
                          display.
                        </TooltipContent>
                      </Tooltip>
                    </TooltipProvider>
                  )}
                </div>
                <LogMessageView message={log.output_message} />
              </>
            )}
            {log.embedding_output && (
              <>
                <div className="mt-4 w-full text-center text-sm font-medium">Embedding</div>
                <LogMessageView
                  message={{
                    role: 'assistant',
                    content: JSON.stringify(log.embedding_output, null, 2),
                  }}
                />
              </>
            )}
            {log.error_details?.error.message && (
              <>
                <div className="mt-4 w-full text-center text-sm font-medium">Error</div>
                <div className="w-full rounded-sm border">
                  <div className="border-b px-6 py-2 text-sm font-medium">Error</div>
                  <div className="px-6 py-2 font-mono text-xs">{log.error_details.error.message}</div>
                </div>
              </>
            )}
          </>
        )}
      </SheetContent>
    </Sheet>
  )
}

const BlockHeader = ({ title, icon }: { title: string; icon: React.ReactNode }) => {
  return (
    <div className="flex items-center gap-2">
      {/* {icon} */}
      <div className="text-sm font-medium">{title}</div>
    </div>
  )
}
