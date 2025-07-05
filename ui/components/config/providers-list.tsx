"use client";

import { useState } from "react";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Edit, Trash2, Key, Globe, Zap, AlertTriangle, CheckCircle, Loader2, Plus } from "lucide-react";
import { toast } from "sonner";
import { ProviderResponse } from "@/lib/types/config";
import { apiService } from "@/lib/api";
import { PROVIDER_COLORS, PROVIDER_LABELS } from "@/lib/constants/logs";
import {
	AlertDialog,
	AlertDialogTrigger,
	AlertDialogContent,
	AlertDialogHeader,
	AlertDialogTitle,
	AlertDialogDescription,
	AlertDialogFooter,
	AlertDialogCancel,
	AlertDialogAction,
} from "@/components/ui/alert-dialog";
import { CardHeader, CardTitle, CardDescription } from "../ui/card";
import ProviderForm from "./provider-form";

interface ProvidersListProps {
	providers: ProviderResponse[];
	onRefresh: () => void;
	isLoading: boolean;
}

export default function ProvidersList({ providers, onRefresh, isLoading }: ProvidersListProps) {
	const [showProviderForm, setShowProviderForm] = useState(false);
	const [editingProvider, setEditingProvider] = useState<ProviderResponse | null>(null);
	const [deletingProvider, setDeletingProvider] = useState<string | null>(null);

	const handleDelete = async (providerKey: string) => {
		setDeletingProvider(providerKey);
		const [, error] = await apiService.deleteProvider(providerKey);
		setDeletingProvider(null);

		if (error) {
			toast.error(error);
		} else {
			toast.success("Provider deleted successfully");
			onRefresh();
		}
	};

	if (isLoading) {
		return (
			<div className="flex items-center justify-center p-8">
				<Loader2 className="h-6 w-6 animate-spin" />
				<span className="ml-2">Loading providers...</span>
			</div>
		);
	}

	if (providers.length === 0) {
		return (
			<Alert>
				<AlertTriangle className="h-4 w-4" />
				<AlertDescription>No providers configured. Add your first AI provider to get started.</AlertDescription>
			</Alert>
		);
	}

	const handleAddProvider = () => {
		setEditingProvider(null);
		setShowProviderForm(true);
	};

	const handleEditProvider = (provider: ProviderResponse) => {
		setEditingProvider(provider);
		setShowProviderForm(true);
	};

	const handleProviderSaved = () => {
		setShowProviderForm(false);
		setEditingProvider(null);
		onRefresh();
	};

	return (
		<>
			{showProviderForm && (
				<ProviderForm
					provider={editingProvider}
					onSave={handleProviderSaved}
					onCancel={() => setShowProviderForm(false)}
					existingProviders={providers.map((p) => p.name)}
				/>
			)}
			<CardHeader className="mb-4 px-0">
				<CardTitle className="flex items-center justify-between">
					<div className="flex items-center gap-2">AI Providers</div>
					<Button onClick={handleAddProvider}>
						<Plus className="h-4 w-4" />
						Add Provider
					</Button>
				</CardTitle>
				<CardDescription>Manage AI model providers, their API keys, and configuration settings.</CardDescription>
			</CardHeader>
			<div className="rounded-md border">
				<Table>
					<TableHeader>
						<TableRow>
							<TableHead>Provider</TableHead>
							<TableHead>Concurrency</TableHead>
							<TableHead>Buffer Size</TableHead>
							<TableHead>Max Retries</TableHead>
							<TableHead>API Keys</TableHead>
							<TableHead className="text-right">Actions</TableHead>
						</TableRow>
					</TableHeader>
					<TableBody>
						{providers.map((provider) => (
							<TableRow key={provider.name}>
								<TableCell>
									<div className="flex items-center space-x-3">
										<div className={`h-3 w-3 rounded-full ${PROVIDER_COLORS[provider.name] || "bg-gray-400"}`} />
										<div>
											<p className="font-medium">{PROVIDER_LABELS[provider.name] || provider.name}</p>
											<p className="text-muted-foreground text-sm">{provider.name}</p>
										</div>
									</div>
								</TableCell>
								<TableCell>
									<div className="flex items-center space-x-2">
										<Badge variant="outline">{provider.concurrency_and_buffer_size?.concurrency || 1}</Badge>
									</div>
								</TableCell>
								<TableCell>
									<div className="flex items-center space-x-2">
										<Badge variant="outline">{provider.concurrency_and_buffer_size?.buffer_size || 10}</Badge>
									</div>
								</TableCell>
								<TableCell>
									<div className="flex items-center space-x-2">
										<Badge variant="outline">{provider.network_config?.max_retries || 0}</Badge>
									</div>
								</TableCell>
								<TableCell>
									<div className="flex items-center space-x-2">
										{provider.name !== "vertex" && provider.name !== "ollama" ? (
											<>
												<Key className="text-muted-foreground h-4 w-4" />
												<span className="text-sm">{provider.keys?.length || 0} keys</span>
											</>
										) : (
											<span className="text-sm">N/A</span>
										)}
									</div>
								</TableCell>
								<TableCell className="text-right">
									<div className="flex items-center justify-end space-x-2">
										<Button variant="outline" size="sm" onClick={() => handleEditProvider(provider)}>
											<Edit className="h-4 w-4" />
										</Button>
										<AlertDialog>
											<AlertDialogTrigger asChild>
												<Button variant="outline" size="sm" disabled={deletingProvider === provider.name}>
													{deletingProvider === provider.name ? (
														<Loader2 className="h-4 w-4 animate-spin" />
													) : (
														<Trash2 className="h-4 w-4" />
													)}
												</Button>
											</AlertDialogTrigger>
											<AlertDialogContent>
												<AlertDialogHeader>
													<AlertDialogTitle>Delete Provider</AlertDialogTitle>
													<AlertDialogDescription>
														Are you sure you want to delete provider {provider.name}? This action cannot be undone.
													</AlertDialogDescription>
												</AlertDialogHeader>
												<AlertDialogFooter>
													<AlertDialogCancel>Cancel</AlertDialogCancel>
													<AlertDialogAction onClick={() => handleDelete(provider.name)}>Delete</AlertDialogAction>
												</AlertDialogFooter>
											</AlertDialogContent>
										</AlertDialog>
									</div>
								</TableCell>
							</TableRow>
						))}
					</TableBody>
				</Table>
			</div>
		</>
	);
}
