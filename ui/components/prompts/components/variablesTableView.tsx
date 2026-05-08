import { AutoSizeTextarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import type { VariableMap } from "@/lib/message";
import { useCallback, useMemo } from "react";
import { Trans, useTranslation } from "react-i18next";

export function VariablesTableView({
	variables,
	onChange,
}: {
	variables: VariableMap;
	onChange: React.Dispatch<React.SetStateAction<VariableMap>>;
}) {
	const { t } = useTranslation();
	const entries = useMemo(() => Object.entries(variables).sort(([a], [b]) => a.localeCompare(b)), [variables]);

	const handleValueChange = useCallback(
		(name: string, value: string) => {
			onChange((prev) => ({ ...prev, [name]: value }));
		},
		[onChange],
	);

	return (
		<div className="flex flex-col gap-3">
			<Label className="text-muted-foreground text-xs font-medium uppercase">{t("workspace.promptRepository.variables.title")}</Label>
			<p className="text-muted-foreground text-xs">
				<Trans
					i18nKey="workspace.promptRepository.variables.description"
					components={{ code: <code className="bg-muted rounded px-1" /> }}
				/>
			</p>
			<div className="border-border overflow-hidden rounded-sm border">
				<table className="w-full table-fixed text-sm">
					<thead>
						<tr className="bg-muted/50 border-border border-b">
							<th className="text-muted-foreground w-[40%] max-w-[40%] px-3 py-1.5 text-left text-xs font-medium">
								{t("workspace.promptRepository.variables.variable")}
							</th>
							<th className="text-muted-foreground px-3 py-1.5 text-left text-xs font-medium">
								{t("workspace.promptRepository.variables.value")}
							</th>
						</tr>
					</thead>
					<tbody>
						{entries.map(([name, value]) => (
							<tr key={name} className="border-border border-b last:border-b-0">
								<td className="w-[40%] max-w-[40%] px-3 py-1.5 align-top">
									<span className="block truncate pt-1 text-xs">{name}</span>
								</td>
								<td className="py-1">
									<AutoSizeTextarea
										value={value}
										onChange={(e) => handleValueChange(name, e.target.value)}
										placeholder={t("workspace.promptRepository.variables.valuePlaceholder")}
										minRows={1}
										className="min-h-0 w-full resize-none border-none bg-transparent px-3 py-1 text-xs shadow-none outline-none focus-visible:ring-0"
									/>
								</td>
							</tr>
						))}
					</tbody>
				</table>
			</div>
		</div>
	);
}