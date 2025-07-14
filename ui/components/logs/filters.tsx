import { Input } from '@/components/ui/input'
import { Button } from '@/components/ui/button'
import { Search, Check } from 'lucide-react'
import { Command, CommandEmpty, CommandGroup, CommandInput, CommandItem, CommandList } from '@/components/ui/command'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'
import { useState, useCallback, useRef, useEffect } from 'react'
import { PROVIDERS, REQUEST_TYPE_LABELS, REQUEST_TYPES, STATUSES } from '@/lib/constants/logs'
import type { LogFilters, Pagination } from '@/lib/types/logs'

interface LogFiltersProps {
  filters: LogFilters
  onFiltersChange: (filters: LogFilters) => void
}

const FILTER_OPTIONS = {
  Status: STATUSES,
  Providers: PROVIDERS,
  Type: REQUEST_TYPES,
} as const

export function LogFilters({ filters, onFiltersChange }: LogFiltersProps) {
  const [open, setOpen] = useState(false)
  const [localSearch, setLocalSearch] = useState(filters.content_search || '')
  const searchTimeoutRef = useRef<NodeJS.Timeout | undefined>(undefined)

  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      if (searchTimeoutRef.current) {
        clearTimeout(searchTimeoutRef.current)
      }
    }
  }, [])

  const handleSearchChange = useCallback(
    (value: string) => {
      setLocalSearch(value)

      // Clear existing timeout
      if (searchTimeoutRef.current) {
        clearTimeout(searchTimeoutRef.current)
      }

      // Set new timeout
      searchTimeoutRef.current = setTimeout(() => {
        onFiltersChange({ ...filters, content_search: value })
      }, 500) // 500ms debounce
    },
    [filters, onFiltersChange],
  )

  const handleFilterSelect = (category: keyof typeof FILTER_OPTIONS, value: string) => {
    const filterKeyMap: Record<keyof typeof FILTER_OPTIONS, keyof LogFilters> = {
      Status: 'status',
      Providers: 'providers',
      Type: 'objects',
    }

    const filterKey = filterKeyMap[category]
    const currentValues = (filters[filterKey] as string[]) || []
    const newValues = currentValues.includes(value) ? currentValues.filter((v) => v !== value) : [...currentValues, value]

    onFiltersChange({
      ...filters,
      [filterKey]: newValues,
    })
  }

  const isSelected = (category: string, value: string) => {
    const filterKeyMap: Record<string, keyof LogFilters> = {
      Status: 'status',
      Providers: 'providers',
      Type: 'objects',
    }

    const filterKey = filterKeyMap[category]
    const currentValues = filters[filterKey]
    return Array.isArray(currentValues) && currentValues.includes(value)
  }

  const getSelectedCount = () => {
    return Object.entries(filters).reduce((count, [key, value]) => {
      if (Array.isArray(value)) {
        return count + value.length
      }
      return count + (value ? 1 : 0)
    }, 0)
  }

  return (
    <div className="flex items-center justify-between space-x-4">
      <div className="flex flex-1 items-center gap-2">
        <Search className="size-5" />
        <Input
          type="text"
          className="border-none shadow-none outline-none focus-visible:ring-0"
          placeholder="Search logs"
          value={localSearch}
          onChange={(e) => handleSearchChange(e.target.value)}
        />
      </div>

      <Popover open={open} onOpenChange={setOpen}>
        <PopoverTrigger asChild>
          <Button variant="outline" size="sm" className="h-9">
            Filters
            {getSelectedCount() > 0 && (
              <span className="bg-primary/10 flex h-6 w-6 items-center justify-center rounded-full text-xs font-normal">
                {getSelectedCount()}
              </span>
            )}
          </Button>
        </PopoverTrigger>
        <PopoverContent className="w-[200px] p-0" align="end">
          <Command>
            <CommandInput placeholder="Search filters..." />
            <CommandList>
              <CommandEmpty>No filters found.</CommandEmpty>
              {Object.entries(FILTER_OPTIONS).map(([category, values]) => (
                <CommandGroup key={category} heading={category}>
                  {values.map((value) => {
                    const selected = isSelected(category, value)
                    return (
                      <CommandItem key={value} onSelect={() => handleFilterSelect(category as keyof typeof FILTER_OPTIONS, value)}>
                        <div
                          className={cn(
                            'border-primary mr-2 flex h-4 w-4 items-center justify-center rounded-sm border',
                            selected ? 'bg-primary text-primary-foreground' : 'opacity-50 [&_svg]:invisible',
                          )}
                        >
                          <Check className="text-primary-foreground size-3" />
                        </div>
                        <span className="lowercase">
                          {category === 'Type' ? REQUEST_TYPE_LABELS[value as keyof typeof REQUEST_TYPE_LABELS] : value}
                        </span>
                      </CommandItem>
                    )
                  })}
                </CommandGroup>
              ))}
            </CommandList>
          </Command>
        </PopoverContent>
      </Popover>
    </div>
  )
}
