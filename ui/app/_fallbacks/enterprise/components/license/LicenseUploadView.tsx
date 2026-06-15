// OSS builds have no license backend; LicenseGate never renders this (the
// status query 404s and the gate passes through). Present only so non-enterprise
// builds compile against the shared @enterprise/components/license import.
export default function LicenseUploadView() {
	return null;
}
