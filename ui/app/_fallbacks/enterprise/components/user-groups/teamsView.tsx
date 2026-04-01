import { Users } from "lucide-react";
import ContactUsView from "../views/contactUsView";

export function TeamsView() {
    return (
        <div className="w-full">
            <ContactUsView
                className="mx-auto min-h-[80vh]"
                icon={<Users className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
                title="Unlock teams governance"
                description="Manage teams, sync from your identity provider, and control access with enterprise-grade governance. This feature is part of the Bifrost enterprise license."
                readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
            />
        </div>
    )
}
