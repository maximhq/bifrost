import AlertingPlaceholderView from "./alertingPlaceholderView";

export default function AlertHistoryView() {
	return (
		<AlertingPlaceholderView
			title="Unlock alerting history for proactive monitoring"
			description="This feature is a part of the Bifrost enterprise license. Review alert delivery outcomes, failures, and resolution events in one place."
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-history"
			testIdPrefix="alert-history"
		/>
	);
}