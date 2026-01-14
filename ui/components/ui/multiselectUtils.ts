import type { ReactNode } from "react";
import {
    ClearIndicatorProps,
    ControlProps,
    DropdownIndicatorProps,
    GroupHeadingProps,
    GroupProps,
    IndicatorSeparatorProps,
    InputProps,
    LoadingIndicatorProps,
    MenuListProps,
    MenuProps,
    MultiValueGenericProps,
    MultiValueProps,
    MultiValueRemoveProps,
    NoticeProps,
    OptionProps,
    PlaceholderProps,
    SingleValueProps,
    ValueContainerProps,
} from "react-select";

export type Option<T> = {
	label: string;
	value: string;
	meta?: T;
};

export type OptionGroup<T> = {
	label: string;
	options: Option<T>[];
};

export type EvaluatorOption<T> = {
	label: string;
	value: string;
	meta?: EvaluatorOptionMeta;
} & T;

export type EvaluatorOptionMeta = {
	description?: string;
	icon?: ReactNode;
};

export type EvaluatorGroup<T> = {
	label: string;
	icon?: ReactNode;
	options: EvaluatorOption<T>[];
};

export interface CustomPlaceholderProps {
	hideSearchIcon?: boolean;
	placeholder?: ReactNode;
}

export interface CustomOptionProps {
	hideSelectedOptions?: boolean;
	checkIconStyling?: string;
	createOptionText?: string;
	dynamicOptionCreation?: boolean;
	hidePlusIcon?: boolean;
}

export interface CustomDropdownIndicatorProps {
	hideDropdownIndicator?: boolean;
}

export interface CustomComponentsProps {
	clearIndicatorView?: (props: ClearIndicatorProps<any>) => ReactNode;
	controlView?: (props: ControlProps<any>) => ReactNode;
	dropdownIndicatorView?: (props: DropdownIndicatorProps<any>) => ReactNode;
	groupView?: (props: GroupProps<any>) => ReactNode;
	groupHeadingView?: (props: GroupHeadingProps<any>) => ReactNode;
	indicatorSeparatorView?: (props: IndicatorSeparatorProps<any>) => ReactNode;
	inputView?: (props: InputProps<any>) => ReactNode;
	loadingIndicatorView?: (props: LoadingIndicatorProps<any>) => ReactNode;
	menuView?: (props: MenuProps<any>) => ReactNode;
	menuListView?: (props: MenuListProps<any>) => ReactNode;
	multiValueView?: (props: MultiValueProps<any>) => ReactNode;
	multiValueContainerView?: (props: MultiValueGenericProps<any>) => ReactNode;
	multiValueLabelView?: (props: MultiValueGenericProps<any>) => ReactNode;
	multiValueRemoveView?: (props: MultiValueRemoveProps<any>) => ReactNode;
	optionView?: (props: OptionProps<any>) => ReactNode;
	noOptionsMessageView?: (props: NoticeProps<any>) => ReactNode;
	placeholderView?: (props: PlaceholderProps<any>) => ReactNode;
	singleValueView?: (props: SingleValueProps<any>) => ReactNode;
	valueContainerView?: (props: ValueContainerProps<any>) => ReactNode;
}

export const createOption = <T = undefined>(label: string): Option<T> => ({
	label,
	value: label,
});