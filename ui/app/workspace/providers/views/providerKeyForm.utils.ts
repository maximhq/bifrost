import { databricksKeyConfigSchema } from "@/lib/types/schemas";
import { z } from "zod";

type DatabricksKeyConfigForm = z.input<typeof databricksKeyConfigSchema>;
type DatabricksKeyConfigPayload = Omit<DatabricksKeyConfigForm, "_auth_type">;

const isSet = (v: { value?: string; ref?: string } | undefined) => Boolean(v?.value || v?.ref);

// stripDatabricksAuthDiscriminator removes the UI-only auth discriminator before the key is
// sent to the API. On the personal access token path the service principal fields are inert,
// so they are dropped: explicitly when the user chose that tab, and otherwise only when the
// pair is incomplete. A complete pair with no discriminator is a valid OAuth M2M key whose
// discriminator was never re-seeded (it is set on mount and after the key resolves), and
// stripping its credentials would silently break the key on save.
export const stripDatabricksAuthDiscriminator = (config: DatabricksKeyConfigForm): DatabricksKeyConfigPayload => {
	const { _auth_type, ...rest } = config;
	const hasPair = isSet(rest.client_id) && isSet(rest.client_secret);
	if (_auth_type === "pat" || (_auth_type !== "oauth_m2m" && !hasPair)) {
		delete rest.client_id;
		delete rest.client_secret;
	}
	return rest;
};
