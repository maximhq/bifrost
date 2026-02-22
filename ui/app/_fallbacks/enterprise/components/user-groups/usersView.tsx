import { Users } from "lucide-react";
import ContactUsView from "../views/contactUsView";


export default function UsersView() {
    return (
        <div className="w-full">
            <ContactUsView
                className="mx-auto min-h-[65vh]"
                icon={<Users className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
                title="Unlock users & user governance"
                description="Manage users, set per-user budgets and rate limits, and control access with enterprise-grade governance. This feature is part of the Bifrost enterprise license."
                readmeLink="https://docs.getbifrost.ai/enterprise/advanced-governance"
            />
        </div>
    )
}