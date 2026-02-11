"use client";

import {
	AlertDialog,
	AlertDialogAction,
	AlertDialogCancel,
	AlertDialogContent,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogHeader,
	AlertDialogTitle,
} from "@/components/ui/alertDialog";
import { Button } from "@/components/ui/button";
import { CardHeader, CardTitle } from "@/components/ui/card";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdownMenu";
import { Switch } from "@/components/ui/switch";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { getErrorMessage, useAppSelector, useUpdatePluginMutation } from "@/lib/store";
import { OtelConfigSchema, OtelProfileConfigSchema } from "@/lib/types/schemas";
import { RbacOperation, RbacResource, useRbac } from "@enterprise/lib";
import { EllipsisIcon, PencilIcon, PlusIcon, TrashIcon } from "lucide-react";
import { useMemo, useState } from "react";
import { toast } from "sonner";
import OtelProfileSheet from "../../dialogs/otelProfileSheet";

// normalizeOtelConfig converts legacy flat config to profiles-based config
function normalizeOtelConfig(config: any): OtelConfigSchema {
	if (!config) {
		return { profiles: [] };
	}
	if (Array.isArray(config.profiles)) {
		return { profiles: config.profiles };
	}
	// Legacy flat config: wrap in a single profile
	const profile: OtelProfileConfigSchema = {
		name: "default",
		enabled: true,
		...config,
	};
	return { profiles: [profile] };
}

export default function OtelView() {
	const selectedPlugin = useAppSelector((state) => state.plugin.selectedPlugin);
	const config = useMemo(() => normalizeOtelConfig(selectedPlugin?.config), [selectedPlugin]);
	const [updatePlugin, { isLoading: isUpdatingPlugin }] = useUpdatePluginMutation();
	const hasUpdateAccess = useRbac(RbacResource.Observability, RbacOperation.Update);

	const [showSheet, setShowSheet] = useState(false);
	const [editingIndex, setEditingIndex] = useState<number | null>(null);
	const [showDeleteDialog, setShowDeleteDialog] = useState<{ show: boolean; index: number } | undefined>(undefined);

	const editingProfile = useMemo(
		() => (editingIndex !== null ? (config.profiles[editingIndex] ?? null) : null),
		[editingIndex, config.profiles],
	);

	const saveProfiles = (profiles: OtelProfileConfigSchema[]) => {
		return updatePlugin({
			name: "otel",
			data: {
				enabled: true,
				config: { profiles },
			},
		}).unwrap();
	};

	const handleAdd = () => {
		setEditingIndex(null);
		setShowSheet(true);
	};

	const handleEdit = (index: number) => {
		setEditingIndex(index);
		setShowSheet(true);
	};

	const handleSaved = () => {
		setShowSheet(false);
		setEditingIndex(null);
	};

	const handleToggleEnabled = (index: number, checked: boolean) => {
		const updatedProfiles = config.profiles.map((p, i) => (i === index ? { ...p, enabled: checked } : p));
		saveProfiles(updatedProfiles)
			.then(() => {
				toast.success(`Profile ${checked ? "enabled" : "disabled"} successfully`);
			})
			.catch((err) => {
				toast.error("Failed to update profile", { description: getErrorMessage(err) });
			});
	};

	const handleDelete = (index: number) => {
		const updatedProfiles = config.profiles.filter((_, i) => i !== index);
		saveProfiles(updatedProfiles)
			.then(() => {
				toast.success("Profile deleted successfully");
				setShowDeleteDialog(undefined);
			})
			.catch((err) => {
				toast.error("Failed to delete profile", { description: getErrorMessage(err) });
			});
	};

	const handleProfileSave = (profile: OtelProfileConfigSchema) => {
		let updatedProfiles: OtelProfileConfigSchema[];
		if (editingIndex !== null) {
			updatedProfiles = config.profiles.map((p, i) => (i === editingIndex ? profile : p));
		} else {
			updatedProfiles = [...config.profiles, profile];
		}
		return saveProfiles(updatedProfiles);
	};

	return (
		<div className="flex w-full flex-col gap-4">
			{showDeleteDialog && (
				<AlertDialog open={showDeleteDialog.show}>
					<AlertDialogContent onClick={(e) => e.stopPropagation()}>
						<AlertDialogHeader>
							<AlertDialogTitle>Delete Profile</AlertDialogTitle>
							<AlertDialogDescription>Are you sure you want to delete this profile? This action cannot be undone.</AlertDialogDescription>
						</AlertDialogHeader>
						<AlertDialogFooter className="pt-4">
							<AlertDialogCancel onClick={() => setShowDeleteDialog(undefined)} disabled={isUpdatingPlugin}>
								Cancel
							</AlertDialogCancel>
							<AlertDialogAction disabled={isUpdatingPlugin || !hasUpdateAccess} onClick={() => handleDelete(showDeleteDialog.index)}>
								Delete
							</AlertDialogAction>
						</AlertDialogFooter>
					</AlertDialogContent>
				</AlertDialog>
			)}
			{showSheet && (
				<OtelProfileSheet
					profile={editingProfile}
					onSave={async (profile: OtelProfileConfigSchema) => {
						return handleProfileSave(profile).then(() => {
							handleSaved();
						});
					}}
					onCancel={() => {
						setShowSheet(false);
						setEditingIndex(null);
					}}
				/>
			)}
			<CardHeader className="px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">OTEL Profiles</div>
					<Button disabled={!hasUpdateAccess} onClick={handleAdd}>
						<PlusIcon className="h-4 w-4" />
						Add profile
					</Button>
				</CardTitle>
			</CardHeader>
			<div className="w-full rounded-sm border">
				<Table className="w-full">
					<TableHeader className="w-full">
						<TableRow>
							<TableHead>Name</TableHead>
							<TableHead>Collector URL</TableHead>
							<TableHead>Protocol</TableHead>
							<TableHead>Enabled</TableHead>
							<TableHead className="text-right"></TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{config.profiles.length === 0 && (
							<TableRow>
								<TableCell colSpan={5} className="py-6 text-center">
									No profiles configured. Add a profile to start sending traces.
								</TableCell>
							</TableRow>
						)}
						{config.profiles.map((profile, index) => (
							<TableRow key={index} className="text-sm transition-colors">
								<TableCell>
									<span className="font-mono text-sm">{profile.name || `Profile ${index + 1}`}</span>
								</TableCell>
								<TableCell>
									<span className="text-muted-foreground font-mono text-sm">{profile.collector_url || "-"}</span>
								</TableCell>
								<TableCell>
									<span className="text-sm uppercase">{profile.protocol || "-"}</span>
								</TableCell>
								<TableCell>
									<Switch
										checked={profile.enabled ?? true}
										size="md"
										disabled={!hasUpdateAccess}
										onCheckedChange={(checked) => handleToggleEnabled(index, checked)}
									/>
								</TableCell>
								<TableCell className="text-right">
									<DropdownMenu>
										<DropdownMenuTrigger asChild>
											<Button onClick={(e) => e.stopPropagation()} variant="ghost">
												<EllipsisIcon className="h-5 w-5" />
											</Button>
										</DropdownMenuTrigger>
										<DropdownMenuContent align="end">
											<DropdownMenuItem onClick={() => handleEdit(index)} disabled={!hasUpdateAccess}>
												<PencilIcon className="mr-1 h-4 w-4" />
												Edit
											</DropdownMenuItem>
											<DropdownMenuItem onClick={() => setShowDeleteDialog({ show: true, index })} disabled={!hasUpdateAccess}>
												<TrashIcon className="mr-1 h-4 w-4" />
												Delete
											</DropdownMenuItem>
										</DropdownMenuContent>
									</DropdownMenu>
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			</div>
		</div>
	);
}
