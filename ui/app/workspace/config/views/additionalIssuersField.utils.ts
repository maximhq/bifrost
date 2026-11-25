import { SecretVar } from "@/lib/types/schemas";

// sameIssuer compares two issuer entries as the server stores them: a reference by its ref and type,
// since a re-typed reference carries no resolved value, and a plain URL by its value.
export const sameIssuer = (a?: SecretVar, b?: SecretVar): boolean => {
	if (a?.ref || b?.ref) return (a?.ref ?? "") === (b?.ref ?? "") && (a?.type ?? "plain_text") === (b?.type ?? "plain_text");
	return (a?.value ?? "") === (b?.value ?? "");
};