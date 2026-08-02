import AlertingPlaceholderView from "./alertingPlaceholderView";

export default function AlertChannelsView() {
	return (
		<AlertingPlaceholderView
			title="Unlock alerting channels for proactive monitoring"
			description="This feature is a part of the Bifrost enterprise license. Configure Slack, PagerDuty, OpsGenie, and webhook alerts to stay ahead of budget and performance issues."
			readmeLink="https://docs.getbifrost.ai/enterprise/alerting/alert-channels"
			testIdPrefix="alert-channels"
		/>
	);
}
