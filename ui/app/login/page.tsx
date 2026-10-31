import LoginView from "@enterprise/components/login/loginView";
import { getRouteApi } from "@tanstack/react-router";
import SetupTokenView from "./views/setupTokenView";

const loginRoute = getRouteApi("/login");

export default function LoginPage() {
	const loaderData = loginRoute.useLoaderData();
	return (
		<div className="overflow-hidden">
			{loaderData?.setupRequired ? <SetupTokenView setupTokenConfigured={loaderData.setupTokenConfigured} /> : <LoginView />}
		</div>
	);
}