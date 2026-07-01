import os
from urllib.parse import quote

import httpx


BASE_URL = os.getenv("BIFROST_URL", "http://localhost:8080").rstrip("/")
MODEL = os.getenv("MODEL", "gemini-2.5-flash")
API_KEY = os.getenv("BIFROST_API_KEY", "")
FILENAME = "tiny.pdf"
MIME_TYPE = "application/pdf"


def tiny_pdf() -> bytes:
    stream = b"BT /F1 12 Tf 50 100 Td (ok) Tj ET"
    objects = [
        b"<< /Type /Catalog /Pages 2 0 R >>",
        b"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
        b"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 200 200] "
        b"/Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
        b"<< /Length " + str(len(stream)).encode() + b" >>\nstream\n" + stream + b"\nendstream",
        b"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
    ]
    pdf = b"%PDF-1.4\n"
    offsets = [0]
    for number, obj in enumerate(objects, start=1):
        offsets.append(len(pdf))
        pdf += f"{number} 0 obj\n".encode() + obj + b"\nendobj\n"
    xref = len(pdf)
    pdf += f"xref\n0 {len(offsets)}\n".encode()
    pdf += b"0000000000 65535 f \n"
    pdf += b"".join(f"{offset:010d} 00000 n \n".encode() for offset in offsets[1:])
    pdf += (
        f"trailer\n<< /Size {len(offsets)} /Root 1 0 R >>\n"
        f"startxref\n{xref}\n%%EOF\n"
    ).encode()
    return pdf


def headers(extra: dict[str, str] | None = None) -> dict[str, str]:
    out: dict[str, str] = {}
    if API_KEY:
        out["x-goog-api-key"] = API_KEY
        out["x-bf-vk"] = API_KEY
    if extra:
        out.update(extra)
    return out


def start_upload(client: httpx.Client, payload: bytes) -> str:
    response = client.post(
        f"{BASE_URL}/genai/upload/v1beta/files",
        headers=headers(
            {
                "Content-Type": "application/json",
                "X-Goog-Upload-Protocol": "resumable",
                "X-Goog-Upload-Command": "start",
                "X-Goog-Upload-Header-Content-Length": str(len(payload)),
                "X-Goog-Upload-Header-Content-Type": MIME_TYPE,
                "X-Goog-Upload-File-Name": FILENAME,
            }
        ),
        json={"file": {"displayName": FILENAME}},
    )
    print(f"start upload status={response.status_code}")
    print(response.text)
    response.raise_for_status()

    upload_url = response.headers["X-Goog-Upload-URL"]
    print(f"upload url={upload_url}")
    return upload_url


def finalize_upload(client: httpx.Client, upload_url: str, payload: bytes) -> dict:
    response = client.post(
        upload_url,
        headers=headers(
            {
                "Content-Length": str(len(payload)),
                "Content-Type": MIME_TYPE,
                "X-Goog-Upload-Command": "upload, finalize",
                "X-Goog-Upload-Offset": "0",
            }
        ),
        content=payload,
    )
    print(f"finalize upload status={response.status_code}")
    print(response.text)
    response.raise_for_status()
    return response.json()["file"]


def retrieve_file(client: httpx.Client, file_name: str) -> dict:
    response = client.get(
        f"{BASE_URL}/genai/v1beta/{file_name}",
        headers=headers(),
    )
    print(f"retrieve status={response.status_code}")
    print(response.text)
    response.raise_for_status()
    return response.json()


def delete_file(client: httpx.Client, file_name: str) -> None:
    response = client.delete(
        f"{BASE_URL}/genai/v1beta/{quote(file_name, safe='/')}",
        headers=headers(),
    )
    print(f"delete status={response.status_code}")


def generate_content(client: httpx.Client, file_uri: str) -> None:
    response = client.post(
        f"{BASE_URL}/genai/v1beta/models/{MODEL}:generateContent",
        headers=headers({"Content-Type": "application/json"}),
        json={
            "contents": [
                {
                    "role": "user",
                    "parts": [
                        {
                            "text": "Read the attached PDF and reply with exactly the lowercase word in it."
                        },
                        {
                            "fileData": {
                                "fileUri": file_uri,
                                "mimeType": MIME_TYPE,
                            }
                        },
                    ],
                }
            ],
            "generationConfig": {"temperature": 0, "maxOutputTokens": 16},
        },
    )
    print(f"generateContent status={response.status_code}")
    print(response.text)


def main() -> None:
    payload = tiny_pdf()
    with httpx.Client(timeout=60) as client:
        upload_url = start_upload(client, payload)
        uploaded = finalize_upload(client, upload_url, payload)
        file_name = uploaded["name"]
        try:
            retrieved = retrieve_file(client, file_name)
            print(f"uploaded mimeType={uploaded.get('mimeType')!r}")
            print(f"retrieved mimeType={retrieved.get('mimeType')!r}")
            generate_content(client, uploaded["uri"])
        finally:
            delete_file(client, file_name)


if __name__ == "__main__":
    main()
