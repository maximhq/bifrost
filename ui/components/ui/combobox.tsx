"use client"

import * as React from "react"
import { Combobox as ComboboxPrimitive } from "@base-ui/react"
import { CheckIcon, ChevronDownIcon, XIcon } from "lucide-react"

import { cn } from "@/lib/utils"
import { Button } from "@/components/ui/button"
import {
  InputGroup,
  InputGroupAddon,
  InputGroupButton,
  InputGroupInput,
} from "@/components/ui/input-group"

const Combobox = ComboboxPrimitive.Root

function ComboboxValue({ ...props }: ComboboxPrimitive.Value.Props) {
  return <ComboboxPrimitive.Value data-slot="combobox-value" {...props} />
}

function ComboboxTrigger({
  className,
  children,
  ...props
}: ComboboxPrimitive.Trigger.Props) {
  return (
    <ComboboxPrimitive.Trigger
      data-slot="combobox-trigger"
      className={cn("[&_svg:not([class*='size-'])]:size-4", className)}
      {...props}
    >
      {children}
      <ChevronDownIcon
        data-slot="combobox-trigger-icon"
        className="text-muted-foreground pointer-events-none size-4"
      />
    </ComboboxPrimitive.Trigger>
  )
}

function ComboboxClear({ className, ...props }: ComboboxPrimitive.Clear.Props) {
  return (
    <ComboboxPrimitive.Clear
      data-slot="combobox-clear"
      render={<InputGroupButton variant="ghost" size="icon-xs" />}
      className={cn(className)}
      {...props}
    >
      <XIcon className="pointer-events-none" />
    </ComboboxPrimitive.Clear>
  )
}

function ComboboxInput({
  className,
  children,
  disabled = false,
  showTrigger = true,
  showClear = false,
  ...props
}: ComboboxPrimitive.Input.Props & {
  showTrigger?: boolean
  showClear?: boolean
}) {
  return (
    <InputGroup className={cn("w-auto", className)}>
      <ComboboxPrimitive.Input
        render={<InputGroupInput disabled={disabled} />}
        {...props}
      />
      <InputGroupAddon align="inline-end">
        {showTrigger && (
          <InputGroupButton
            size="icon-xs"
            variant="ghost"
            asChild
            data-slot="input-group-button"
            className="group-has-data-[slot=combobox-clear]/input-group:hidden data-pressed:bg-transparent"
            disabled={disabled}
          >
            <ComboboxTrigger />
          </InputGroupButton>
        )}
        {showClear && <ComboboxClear disabled={disabled} />}
      </InputGroupAddon>
      {children}
    </InputGroup>
  )
}

function ComboboxContent({
  className,
  side = "bottom",
  sideOffset = 6,
  align = "start",
  alignOffset = 0,
  anchor,
  ...props
}: ComboboxPrimitive.Popup.Props &
  Pick<
    ComboboxPrimitive.Positioner.Props,
    "side" | "align" | "sideOffset" | "alignOffset" | "anchor"
  >) {
  return (
    <ComboboxPrimitive.Portal>
      <ComboboxPrimitive.Positioner
        side={side}
        sideOffset={sideOffset}
        align={align}
        alignOffset={alignOffset}
        anchor={anchor}
        className="isolate z-50"
      >
        <ComboboxPrimitive.Popup
          data-slot="combobox-content"
          data-chips={!!anchor}
          className={cn(
            "bg-popover text-popover-foreground data-open:animate-in data-closed:animate-out data-closed:fade-out-0 data-open:fade-in-0 data-closed:zoom-out-95 data-open:zoom-in-95 data-[side=bottom]:slide-in-from-top-2 data-[side=left]:slide-in-from-right-2 data-[side=right]:slide-in-from-left-2 data-[side=top]:slide-in-from-bottom-2 ring-foreground/10 *:data-[slot=input-group]:bg-input/30 *:data-[slot=input-group]:border-input/30 group/combobox-content relative max-h-96 w-(--anchor-width) max-w-(--available-width) min-w-[calc(var(--anchor-width)+--spacing(7))] origin-(--transform-origin) overflow-hidden rounded-md shadow-md ring-1 duration-100 data-[chips=true]:min-w-(--anchor-width) *:data-[slot=input-group]:m-1 *:data-[slot=input-group]:mb-0 *:data-[slot=input-group]:h-8 *:data-[slot=input-group]:shadow-none",
            className
          )}
          {...props}
        />
      </ComboboxPrimitive.Positioner>
    </ComboboxPrimitive.Portal>
  )
}

function ComboboxList({ className, ...props }: ComboboxPrimitive.List.Props) {
  return (
    <ComboboxPrimitive.List
      data-slot="combobox-list"
      className={cn(
        "max-h-[min(calc(--spacing(96)---spacing(9)),calc(var(--available-height)---spacing(9)))] scroll-py-1 overflow-y-auto p-1",
        className
      )}
      {...props}
    />
  )
}

function ComboboxItem({
  className,
  children,
  ...props
}: ComboboxPrimitive.Item.Props) {
  return (
    <ComboboxPrimitive.Item
      data-slot="combobox-item"
      className={cn(
        "data-highlighted:bg-accent data-highlighted:text-accent-foreground relative flex w-full cursor-default items-center gap-2 rounded-sm py-1.5 pr-8 pl-2 text-sm outline-hidden select-none data-[disabled]:pointer-events-none data-[disabled]:opacity-50 [&_svg]:pointer-events-none [&_svg]:shrink-0 [&_svg:not([class*='size-'])]:size-4",
        className
      )}
      {...props}
    >
      {children}
      <ComboboxPrimitive.ItemIndicator
        data-slot="combobox-item-indicator"
        render={
          <span className="pointer-events-none absolute right-2 flex size-4 items-center justify-center" />
        }
      >
        <CheckIcon className="pointer-events-none size-4 pointer-coarse:size-5" />
      </ComboboxPrimitive.ItemIndicator>
    </ComboboxPrimitive.Item>
  )
}

function ComboboxGroup({ className, ...props }: ComboboxPrimitive.Group.Props) {
  return (
    <ComboboxPrimitive.Group
      data-slot="combobox-group"
      className={cn(className)}
      {...props}
    />
  )
}

function ComboboxLabel({
  className,
  ...props
}: ComboboxPrimitive.GroupLabel.Props) {
  return (
    <ComboboxPrimitive.GroupLabel
      data-slot="combobox-label"
      className={cn(
        "text-muted-foreground px-2 py-1.5 text-xs pointer-coarse:px-3 pointer-coarse:py-2 pointer-coarse:text-sm",
        className
      )}
      {...props}
    />
  )
}

function ComboboxCollection({ ...props }: ComboboxPrimitive.Collection.Props) {
  return (
    <ComboboxPrimitive.Collection data-slot="combobox-collection" {...props} />
  )
}

function ComboboxEmpty({ className, ...props }: ComboboxPrimitive.Empty.Props) {
  return (
    <ComboboxPrimitive.Empty
      data-slot="combobox-empty"
      className={cn(
        "text-muted-foreground hidden w-full justify-center py-2 text-center text-sm group-data-empty/combobox-content:flex",
        className
      )}
      {...props}
    />
  )
}

function ComboboxSeparator({
  className,
  ...props
}: ComboboxPrimitive.Separator.Props) {
  return (
    <ComboboxPrimitive.Separator
      data-slot="combobox-separator"
      className={cn("bg-border -mx-1 my-1 h-px", className)}
      {...props}
    />
  )
}

function ComboboxChips({
  className,
  ...props
}: React.ComponentPropsWithRef<typeof ComboboxPrimitive.Chips> &
  ComboboxPrimitive.Chips.Props) {
  return (
    <ComboboxPrimitive.Chips
      data-slot="combobox-chips"
      className={cn(
        "dark:bg-input/30 border-input focus-within:border-ring focus-within:ring-ring/50 has-aria-invalid:ring-destructive/20 dark:has-aria-invalid:ring-destructive/40 has-aria-invalid:border-destructive dark:has-aria-invalid:border-destructive/50 flex min-h-9 flex-wrap items-center gap-1.5 rounded-md border bg-transparent bg-clip-padding px-2.5 py-1.5 text-sm shadow-xs transition-[color,box-shadow] focus-within:ring-[3px] has-aria-invalid:ring-[3px] has-data-[slot=combobox-chip]:px-1.5",
        className
      )}
      {...props}
    />
  )
}

function ComboboxChip({
  className,
  children,
  showRemove = true,
  ...props
}: ComboboxPrimitive.Chip.Props & {
  showRemove?: boolean
}) {
  return (
    <ComboboxPrimitive.Chip
      data-slot="combobox-chip"
      className={cn(
        "bg-muted text-foreground flex h-[calc(--spacing(5.5))] w-fit items-center justify-center gap-1 rounded-sm px-1.5 text-xs font-medium whitespace-nowrap has-disabled:pointer-events-none has-disabled:cursor-not-allowed has-disabled:opacity-50 has-data-[slot=combobox-chip-remove]:pr-0",
        className
      )}
      {...props}
    >
      {children}
      {showRemove && (
        <ComboboxPrimitive.ChipRemove
          render={<Button variant="ghost" size="icon" />}
          className="-ml-1 opacity-50 hover:opacity-100"
          data-slot="combobox-chip-remove"
        >
          <XIcon className="pointer-events-none" />
        </ComboboxPrimitive.ChipRemove>
      )}
    </ComboboxPrimitive.Chip>
  )
}

function ComboboxChipsInput({
  className,
  children,
  ...props
}: ComboboxPrimitive.Input.Props) {
  return (
    <ComboboxPrimitive.Input
      data-slot="combobox-chip-input"
      className={cn("min-w-16 flex-1 outline-none", className)}
      {...props}
    />
  )
}

function useComboboxAnchor() {
  return React.useRef<HTMLDivElement | null>(null)
}

// ---------------------------------------------------------------------------
// ComboboxSelect — high-level, option-driven combobox with multiselect
// ---------------------------------------------------------------------------

interface ComboboxSelectOption {
  label: string
  value: string
}

interface ComboboxSelectBaseProps {
  /** The list of options to display */
  options: ComboboxSelectOption[]
  /** Placeholder shown when nothing is selected */
  placeholder?: string
  disabled?: boolean
  /** When true the search/filter input is read-only (dropdown-only) */
  disableSearch?: boolean
  /** Hide the clear (×) button */
  hideClear?: boolean
  className?: string
  /** Message shown when filter yields no matches */
  emptyMessage?: string
}

interface ComboboxSelectSingleProps extends ComboboxSelectBaseProps {
  multiple?: false
  value?: string | null
  onValueChange?: (value: string | null) => void
}

interface ComboboxSelectMultiProps extends ComboboxSelectBaseProps {
  multiple: true
  value?: string[]
  onValueChange?: (value: string[]) => void
}

type ComboboxSelectProps = ComboboxSelectSingleProps | ComboboxSelectMultiProps

/**
 * Shared hook that owns the search query, label-resolver, and filtered list.
 * `filter={null}` on the root disables @base-ui's internal filtering so we
 * can drive it ourselves without the stale `data-empty` flag issue.
 */
function useComboboxSelect(
  options: ComboboxSelectOption[],
  disableSearch: boolean,
) {
  const [query, setQuery] = React.useState("")

  const getLabel = React.useCallback(
    (val: string | null) =>
      options.find((o) => o.value === val)?.label ?? val ?? "",
    [options],
  )

  const filtered = React.useMemo(() => {
    if (disableSearch || !query) return options
    const q = query.toLowerCase()
    return options.filter((o) => o.label.toLowerCase().includes(q))
  }, [options, query, disableSearch])

  const handleOpenChange = React.useCallback(
    (open: boolean) => {
      if (open) setQuery("")
    },
    [],
  )

  return { query, setQuery, getLabel, filtered, handleOpenChange }
}

function ComboboxSelect(props: ComboboxSelectProps) {
  const {
    options,
    placeholder = "Select…",
    disabled = false,
    disableSearch = false,
    className,
    emptyMessage = "No results found.",
  } = props

  const { getLabel, filtered, setQuery, handleOpenChange } =
    useComboboxSelect(options, disableSearch)

  if (props.multiple) {
    return (
      <ComboboxSelectMulti
        options={options}
        filtered={filtered}
        value={props.value ?? []}
        onValueChange={props.onValueChange}
        placeholder={placeholder}
        disabled={disabled}
        disableSearch={disableSearch}
        className={className}
        emptyMessage={emptyMessage}
        getLabel={getLabel}
        setQuery={setQuery}
        handleOpenChange={handleOpenChange}
      />
    )
  }

  return (
    <Combobox
      value={props.value ?? null}
      onValueChange={(v) => props.onValueChange?.(v)}
      onOpenChange={handleOpenChange}
      onInputValueChange={(v) => setQuery(v)}
      filter={null}
      itemToStringLabel={getLabel}
    >
      <ComboboxInput
        placeholder={placeholder}
        showClear={!props.hideClear && !!props.value}
        showTrigger
        disabled={disabled}
        className={className}
        readOnly={disableSearch}
      />
      <ComboboxContent>
        <ComboboxList>
          {filtered.map((option) => (
            <ComboboxItem key={option.value} value={option.value}>
              {option.label}
            </ComboboxItem>
          ))}
        </ComboboxList>
        {!disableSearch && filtered.length === 0 && (
          <div className="text-muted-foreground py-6 text-center text-sm">
            {emptyMessage}
          </div>
        )}
      </ComboboxContent>
    </Combobox>
  )
}

/** Inner component for multi-select so the chips anchor ref lives inside a single render scope */
function ComboboxSelectMulti({
  options,
  filtered,
  value,
  onValueChange,
  placeholder,
  disabled,
  disableSearch,
  className,
  emptyMessage,
  getLabel,
  setQuery,
  handleOpenChange,
}: {
  options: ComboboxSelectOption[]
  filtered: ComboboxSelectOption[]
  value: string[]
  onValueChange?: (value: string[]) => void
  placeholder: string
  disabled: boolean
  disableSearch: boolean
  className?: string
  emptyMessage: string
  getLabel: (val: string | null) => string
  setQuery: (q: string) => void
  handleOpenChange: (open: boolean) => void
}) {
  const anchorRef = React.useRef<HTMLDivElement | null>(null)

  return (
    <Combobox
      value={value}
      onValueChange={(v) => onValueChange?.(v)}
      onOpenChange={handleOpenChange}
      onInputValueChange={(v) => setQuery(v)}
      filter={null}
      multiple
      itemToStringLabel={getLabel}
    >
      <ComboboxChips ref={anchorRef} className={className}>
        <ComboboxValue>
          {(selectedValue: string) => (
            <ComboboxChip key={selectedValue}>
              {getLabel(selectedValue)}
            </ComboboxChip>
          )}
        </ComboboxValue>
        <ComboboxChipsInput
          placeholder={value.length === 0 ? placeholder : ""}
          disabled={disabled}
          readOnly={disableSearch}
        />
      </ComboboxChips>
      <ComboboxContent anchor={anchorRef}>
        <ComboboxList>
          {filtered.map((option) => (
            <ComboboxItem key={option.value} value={option.value}>
              {option.label}
            </ComboboxItem>
          ))}
        </ComboboxList>
        {!disableSearch && filtered.length === 0 && (
          <div className="text-muted-foreground py-6 text-center text-sm">
            {emptyMessage}
          </div>
        )}
      </ComboboxContent>
    </Combobox>
  )
}

export {
  Combobox,
  ComboboxInput,
  ComboboxContent,
  ComboboxList,
  ComboboxItem,
  ComboboxGroup,
  ComboboxLabel,
  ComboboxCollection,
  ComboboxEmpty,
  ComboboxSeparator,
  ComboboxChips,
  ComboboxChip,
  ComboboxChipsInput,
  ComboboxTrigger,
  ComboboxValue,
  ComboboxSelect,
  useComboboxAnchor,
}

export type { ComboboxSelectOption, ComboboxSelectProps }
