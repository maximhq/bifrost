import type { UserAccessProfile } from "@enterprise/lib/types/accessProfile";

interface ManagedVirtualKeyNoticeProps {
	isManagedByProfile: boolean;
	managingProfile?: UserAccessProfile;
	ownerGoverned?: boolean;
}

export default function ManagedVirtualKeyNotice(_props: ManagedVirtualKeyNoticeProps) {
	return null;
}