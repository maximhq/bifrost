import { cn } from "@/lib/utils";
import { getSupportedTimezones } from "@/lib/timezones";
import { TZDate, tz, tzName } from "@date-fns/tz";
import { format } from "date-fns";
import { Calendar as CalendarIcon, Globe } from "lucide-react";
import React, { useEffect, useMemo } from "react";
import { DateRange } from "react-day-picker";
import { Button } from "./button";
import { Calendar } from "./calendar";
import { ComboboxSelect } from "./combobox";
import { Label } from "./label";
import { Popover, PopoverContent, PopoverTrigger } from "./popover";
import { TimePicker, TimeValue } from "./timePicker";

export type TimeRange = {
	from: TimeValue;
	to: TimeValue;
};

/**
 * Builds the searchable timezone option list. Each label pairs the IANA id
 * with its short abbreviation in the given zone (e.g. "America/New_York (EST)").
 */
function buildTimezoneOptions(referenceDate: Date): { label: string; value: string }[] {
	const zones = getSupportedTimezones();
	return zones.map((zone) => {
		let abbr = "";
		try {
			abbr = tzName(zone, referenceDate, "short");
		} catch {
			// Some runtimes may not resolve an abbreviation; fall back to id only.
		}
		return { value: zone, label: abbr ? `${zone} (${abbr})` : zone };
	});
}

interface DatePickerWithRangeProps extends React.HTMLAttributes<HTMLDivElement> {
	buttonClassName?: string;
	triggerLabel?: string;
	onTrigger?: (
		e: React.MouseEvent<HTMLButtonElement>,
		range: {
			from: { date?: Date; time: TimeValue };
			to: { date: Date; time: TimeValue };
		},
	) => void;
}

interface DateTimePickerWithRangeProps extends DatePickerWithRangeProps {
	popupAlignment?: "start" | "end" | "center";
	onDateTimeUpdate?: (date: DateRange) => void;
	onPredefinedPeriodChange?: (period: string | undefined) => void;
	dateTime?: DateRange;
	preDefinedPeriods?: { label: string; value: string }[];
	predefinedPeriod?: string;
	disabledBefore?: Date;
	disabledAfter?: Date;
	open?: boolean;
	onOpenChange?: (open: boolean) => void;
	/** Optional data-testid for the trigger button (e.g. for E2E tests) */
	triggerTestId?: string;
	/** Enables the timezone display + selection UI. When false/undefined, dates are handled in browser-local time (unchanged). */
	showTimezone?: boolean;
	/** Controlled active IANA timezone (e.g. "America/New_York"). Only used when showTimezone is true. */
	timezone?: string;
	/** Fired when the user picks a new timezone from the dropdown. */
	onTimezoneChange?: (timezone: string) => void;
}

export function DateTimePickerWithRange(props: DateTimePickerWithRangeProps) {
	const { className, buttonClassName, triggerLabel, onTrigger, dateTime } = props;
	const activeTimezone = props.showTimezone ? props.timezone : undefined;

	/** Extract wall-clock hours/minutes from a Date, interpreting in the active timezone when set. */
	const extractTime = (d: Date | undefined, fallback: TimeValue): TimeValue => {
		if (!d) return fallback;
		if (activeTimezone) {
			// Read wall-clock in the target zone
			const zoned = new TZDate(d.getTime(), activeTimezone);
			return { hour: zoned.getHours(), minute: zoned.getMinutes() };
		}
		return { hour: d.getHours(), minute: d.getMinutes() };
	};

	const [date, setDate] = React.useState<DateRange | undefined>(dateTime);
	const [timeValue, setTimeValue] = React.useState<TimeRange>({
		from: extractTime(dateTime?.from, { hour: 0, minute: 0 }),
		to: extractTime(dateTime?.to, { hour: 23, minute: 59 }),
	});
	const [isOpen, setIsOpen] = React.useState<boolean>(false);
	const [predefinedPeriod, setPredefinedPeriod] = React.useState<string | undefined>(props.predefinedPeriod);

	const timezoneOptions = useMemo(() => (props.showTimezone ? buildTimezoneOptions(new Date()) : []), [props.showTimezone, activeTimezone]);

	const disabledDateRange = useMemo(() => {
		if (!props.disabledBefore && !props.disabledAfter) return undefined;
		let range: any = {};
		if (props.disabledBefore) range["before"] = props.disabledBefore;
		if (props.disabledAfter) range["after"] = props.disabledAfter;
		return range;
	}, [props.disabledBefore, props.disabledAfter]);

	const printTimeValue = (timeObj: TimeValue): string => {
		// Validate input
		if (!timeObj || timeObj.hour < 0 || timeObj.hour >= 24 || timeObj.minute < 0 || timeObj.minute >= 60) {
			return "";
		}

		let hour = ((timeObj.hour + 11) % 12) + 1; // Convert hour to 12-hour format
		let period = timeObj.hour >= 12 ? "PM" : "AM"; // Determine AM/PM
		let minute = timeObj.minute.toString().padStart(2, "0"); // Ensure the minute has two digits

		return `${hour}:${minute} ${period}`;
	};

	/**
	 * Combine a calendar date and a wall-clock time into an absolute Date.
	 * When `activeTimezone` is set, the wall-clock is interpreted in that zone
	 * (via TZDate) so the returned epoch reflects the correct absolute instant.
	 */
	const getDateTime = (date: Date | undefined, time: TimeValue | undefined | null): Date | undefined => {
		if (!date || !time) return undefined;

		if (activeTimezone) {
			// Interpret wall-clock in the target timezone → correct absolute instant.
			const zoned = new TZDate(date.getTime(), activeTimezone);
			return new TZDate(zoned.getFullYear(), zoned.getMonth(), zoned.getDate(), time.hour, time.minute, 0, 0, activeTimezone);
		}

		// Local-time path (unchanged legacy behavior).
		const dateAtMidnight = new Date(date.getFullYear(), date.getMonth(), date.getDate());
		const epochTime = dateAtMidnight.getTime() + time.hour * 60 * 60 * 1000 + time.minute * 60 * 1000;
		return new Date(epochTime);
	};

	/** Format a date for the trigger button, respecting the active timezone. */
	const formatDate = (d: Date, fmt: string): string => {
		if (activeTimezone) return format(d, fmt, { in: tz(activeTimezone) });
		return format(d, fmt);
	};

	/** Short timezone abbreviation for the trigger button label. */
	const tzAbbreviation = useMemo(() => {
		if (!activeTimezone) return "";
		try {
			return tzName(activeTimezone, new Date(), "short");
		} catch {
			return "";
		}
	}, [activeTimezone]);

	useEffect(() => {
		setDate(dateTime);
		setTimeValue({
			from: extractTime(dateTime?.from, { hour: 0, minute: 0 }),
			to: extractTime(dateTime?.to, { hour: 23, minute: 59 }),
		});
		// eslint-disable-next-line react-hooks/exhaustive-deps
	}, [dateTime, activeTimezone]);

	useEffect(() => {
		setPredefinedPeriod(props.predefinedPeriod);
	}, [props.predefinedPeriod]);

	return (
		<div className={cn("grid gap-2", className)}>
			<Popover
				open={props.open !== undefined ? props.open : isOpen}
				onOpenChange={(open) => {
					setIsOpen(open);
					props.onOpenChange && props.onOpenChange(open);
				}}
			>
				<PopoverTrigger asChild>
					<Button
						id="date"
						variant="outline"
						data-testid={props.triggerTestId}
						className={cn(
							"justify-start text-left font-normal",
							!date && "text-content-disabled",
							buttonClassName,
							isOpen && "border-black",
						)}
					>
						<CalendarIcon className="h-4 w-4" strokeWidth={1.5} />
						{predefinedPeriod ? (
							// Relative periods are durations (last hour, last 7 days) and are
							// timezone-independent, so we intentionally omit the timezone badge here.
							<span>{props.preDefinedPeriods?.find((p) => p.value === predefinedPeriod)?.label}</span>
						) : (
							<>
								{dateTime?.from ? (
									dateTime.to ? (
										<>
											{formatDate(dateTime.from, "LLL dd, y")} {printTimeValue(timeValue?.from)} - {formatDate(dateTime.to, "LLL dd, y")}{" "}
											{printTimeValue(timeValue?.to)}
											{props.showTimezone && tzAbbreviation ? ` · ${tzAbbreviation}` : ""}
										</>
									) : (
										formatDate(dateTime.from, "LLL dd, y")
									)
								) : (
									<span>Pick a date</span>
								)}
							</>
						)}
					</Button>
				</PopoverTrigger>
				<PopoverContent className="w-auto p-0" align={props.popupAlignment ? props.popupAlignment : "start"}>
					<div className="flex flex-row gap-2">
						<div>
							<Calendar
								autoFocus
								mode="range"
								disabled={disabledDateRange}
								defaultMonth={date?.from}
								selected={date}
								onSelect={(range) => {
									if (!range) return;
									if (!range.to) {
										// here user has selected single date
										range.to = range.from;
									}
									setDate(range);
									setPredefinedPeriod(undefined);
									// Checking if range is different than props.dateTime
									if (
										range.from?.toISOString() !== props.dateTime?.from?.toISOString() ||
										range.to?.toISOString() !== props.dateTime?.to?.toISOString()
									) {
										props.onPredefinedPeriodChange && props.onPredefinedPeriodChange(undefined);
										// Checking if range is valid
										props.onDateTimeUpdate &&
											props.onDateTimeUpdate({
												from: getDateTime(range.from, timeValue?.from),
												to: getDateTime(range.to, timeValue?.to),
											});
									}
								}}
								numberOfMonths={2}
							/>
							<div className="-mt-1 flex flex-row items-center px-2 pb-1">
								<div className="m-1 flex flex-1 flex-col gap-1">
									<Label className="ml-0.5">From Time</Label>
									<TimePicker
										aria-label="From Time"
										className=""
										value={timeValue?.from}
										onChange={(v) => {
											if (!date || !date.from || !v) return;
											setTimeValue({ from: v, to: timeValue.to });
											const nextFrom = getDateTime(date.from, v);
											if (nextFrom?.toISOString() !== props.dateTime?.from?.toISOString()) {
												// Checking if range is valid
												props.onDateTimeUpdate &&
													props.onDateTimeUpdate({
														from: nextFrom,
														to: getDateTime(date.to, timeValue?.to),
													});
											}
										}}
									/>
								</div>
								<div className="m-1 flex flex-1 flex-col gap-1">
									<Label className="ml-0.5">To Time</Label>
									<TimePicker
										aria-label="To Time"
										className=""
										value={timeValue?.to}
										onChange={(v) => {
											if (!date || !date.to || !v) return;
											setTimeValue({ ...timeValue, to: v });
											const nextTo = getDateTime(date.to, v);
											if (nextTo?.toISOString() !== props.dateTime?.to?.toISOString()) {
												props.onDateTimeUpdate &&
													props.onDateTimeUpdate({
														from: getDateTime(date.from, timeValue?.from),
														to: nextTo,
													});
											}
										}}
									/>
								</div>
							</div>
						</div>
						{props.preDefinedPeriods && (
							<div className="flex w-[150px] flex-col gap-1 border-l py-2 pr-3 pl-2">
								{props.preDefinedPeriods.map((period) => (
									<Button
										className={cn("w-full text-start text-sm", predefinedPeriod === period.value && "bg-primary text-primary-foreground")}
										variant="ghost"
										key={period.value}
										onClick={(e) => {
											e.preventDefault();
											e.stopPropagation();
											setPredefinedPeriod(period.value);
											props.onPredefinedPeriodChange && props.onPredefinedPeriodChange(period.value);
										}}
									>
										{period.label}
									</Button>
								))}
							</div>
						)}
					</div>
					{props.showTimezone && (
						<div className="flex items-center gap-2 border-t px-3 py-2">
							<Globe className="text-muted-foreground size-4 shrink-0" />
							<Label className="text-muted-foreground shrink-0 text-xs">Timezone</Label>
							<div className="ml-auto w-[260px]">
								<ComboboxSelect
									options={timezoneOptions}
									value={activeTimezone ?? null}
									onValueChange={(v) => {
										if (!v) return;
										props.onTimezoneChange?.(v);
										// Wall-clock stays fixed: re-emit the same calendar day + time
										// interpreted in the newly selected zone so the query shifts.
										if (date?.from && date?.to) {
											const fromInActiveTimezone = activeTimezone ? new TZDate(date.from.getTime(), activeTimezone) : date.from;
											const toInActiveTimezone = activeTimezone ? new TZDate(date.to.getTime(), activeTimezone) : date.to;
											const from = new TZDate(
												fromInActiveTimezone.getFullYear(),
												fromInActiveTimezone.getMonth(),
												fromInActiveTimezone.getDate(),
												timeValue.from.hour,
												timeValue.from.minute,
												0,
												0,
												v,
											);
											const to = new TZDate(
												toInActiveTimezone.getFullYear(),
												toInActiveTimezone.getMonth(),
												toInActiveTimezone.getDate(),
												timeValue.to.hour,
												timeValue.to.minute,
												0,
												0,
												v,
											);
											props.onDateTimeUpdate?.({ from, to });
										}
									}}
									hideClear
									placeholder="Select timezone"
									data-testid="datepicker-timezone-select"
								/>
							</div>
						</div>
					)}
					{triggerLabel && onTrigger && (
						<div className="mt-1 mb-2 flex w-full px-3">
							<Button
								className="ml-auto"
								onClick={(e) => {
									if (!date || !date.from || !date.to) return;
									onTrigger(e, {
										from: { date: date.from, time: timeValue.from },
										to: { date: date.to, time: timeValue.to },
									});
								}}
							>
								{triggerLabel}
							</Button>
						</div>
					)}
				</PopoverContent>
			</Popover>
		</div>
	);
}

interface DateTimePickerProps extends React.HTMLAttributes<HTMLDivElement> {
	buttonClassName?: string;
	triggerLabel?: string;
	onTrigger?: (e: React.MouseEvent<HTMLButtonElement>, dateTime: { date?: Date; time: TimeValue }) => void;
	popupAlignment?: "start" | "end" | "center";
	onDateTimeUpdate?: (dateTime: Date) => void;
	dateTime?: Date;
	disabledBefore?: Date;
	disabledAfter?: Date;
}

export function DateTimePicker(props: DateTimePickerProps) {
	const { className, buttonClassName, triggerLabel, onTrigger, dateTime } = props;

	const initialDate = dateTime ? new Date(dateTime) : new Date();
	const [date, setDate] = React.useState<Date | undefined>(initialDate);
	const [timeValue, setTimeValue] = React.useState<TimeValue>({ hour: initialDate.getHours(), minute: initialDate.getMinutes() });
	const [isOpen, setIsOpen] = React.useState<boolean>(false);

	const disabledDateRange = useMemo(() => {
		if (!props.disabledBefore && !props.disabledAfter) return undefined;
		let range: any = {};
		if (props.disabledBefore) range["before"] = props.disabledBefore;
		if (props.disabledAfter) range["after"] = props.disabledAfter;
		return range;
	}, [props.disabledBefore, props.disabledAfter]);

	const printTimeValue = (timeObj: TimeValue): string => {
		// Validate input
		if (!timeObj || timeObj.hour < 0 || timeObj.hour >= 24 || timeObj.minute < 0 || timeObj.minute >= 60) {
			return "";
		}

		let hour = ((timeObj.hour + 11) % 12) + 1; // Convert hour to 12-hour format
		let period = timeObj.hour >= 12 ? "PM" : "AM"; // Determine AM/PM
		let minute = timeObj.minute.toString().padStart(2, "0"); // Ensure the minute has two digits

		return `${hour}:${minute} ${period}`;
	};

	const getDateTime = (date: Date | undefined | null, time: TimeValue | undefined | null): Date | undefined => {
		if (!date) return undefined;
		const dateTime = new Date(date);
		if (time) dateTime.setHours(time.hour, time.minute);
		return dateTime;
	};

	useEffect(() => {
		if (dateTime) {
			const newDate = new Date(dateTime);
			setDate(newDate);
			setTimeValue({ hour: newDate.getHours(), minute: newDate.getMinutes() });
		}
	}, [dateTime]);

	return (
		<div className={cn("grid gap-2", className)}>
			<Popover
				modal={true}
				onOpenChange={(open) => {
					setIsOpen(open);
				}}
			>
				<PopoverTrigger asChild>
					<Button
						id="date"
						variant="default"
						className={cn(
							"w-max justify-start text-left font-normal",
							!date && "text-content-disabled",
							buttonClassName,
							isOpen && "border-black",
						)}
					>
						<CalendarIcon className="h-4 w-4" strokeWidth={1.5} />
						{date ? (
							<>
								{format(date, "LLL dd, y")} {printTimeValue(timeValue)}
							</>
						) : (
							<span>Pick a date and time</span>
						)}
					</Button>
				</PopoverTrigger>
				<PopoverContent className="w-auto p-0" align={props.popupAlignment ? props.popupAlignment : "start"}>
					<div className="p-2">
						<Calendar
							autoFocus
							mode="single"
							disabled={disabledDateRange}
							defaultMonth={date}
							selected={date}
							onSelect={(selectedDate) => {
								if (!selectedDate) return;
								setDate(selectedDate);

								const newDateTime = getDateTime(selectedDate, timeValue);

								if (newDateTime?.toISOString() !== props.dateTime?.toISOString()) {
									props.onDateTimeUpdate && newDateTime && props.onDateTimeUpdate(newDateTime);
								}
							}}
						/>
						<div className="mt-3 flex flex-col gap-1 px-2 pb-2">
							<Label className="ml-0.5">Time</Label>
							<TimePicker
								aria-label="Time"
								className=""
								value={timeValue}
								onChange={(v) => {
									if (v) setTimeValue(v);

									const newDateTime = getDateTime(date, v);

									if (newDateTime?.toISOString() !== props.dateTime?.toISOString()) {
										props.onDateTimeUpdate && newDateTime && props.onDateTimeUpdate(newDateTime);
									}
								}}
							/>
						</div>
					</div>
					{triggerLabel && onTrigger && (
						<div className="mt-1 mb-2 flex w-full px-3">
							<Button
								className="ml-auto"
								onClick={(e) =>
									onTrigger(e, {
										date: date,
										time: timeValue,
									})
								}
							>
								{triggerLabel}
							</Button>
						</div>
					)}
				</PopoverContent>
			</Popover>
		</div>
	);
}