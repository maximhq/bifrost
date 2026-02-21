import { Users } from "lucide-react";
import ContactUsView from "../views/contactUsView";


export default function UsersView() {
    return (
        <div className="w-full">
            <ContactUsView
                className="mx-auto min-h-[65vh]"
                icon={<Users className="h-[5.5rem] w-[5.5rem]" strokeWidth={1} />}
                title="Unlock users & user management"
                description="This feature is a part of the Bifrost enterprise license. We would love to know more about your use case and how we can help you."
                readmeLink="https://docs.getbifrost.ai/enterprise/setting-up-okta"
            />
        </div>
    )
}