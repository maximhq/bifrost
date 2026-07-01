import os
from urllib.parse import quote

import httpx


BASE_URL = os.getenv("BIFROST_URL", "http://localhost:8080").rstrip("/")
MODEL = os.getenv("MODEL", "anthropic/claude-sonnet-4-5-20250929")
API_KEY = os.getenv("BIFROST_API_KEY", "")
FILE_ID = os.getenv("FILE_ID")


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


def headers() -> dict[str, str]:
    out = {"anthropic-beta": "files-api-2025-04-14"}
    if API_KEY:
        out["authorization"] = f"Bearer {API_KEY}"
    return out


def upload_file(client: httpx.Client) -> str:
    response = client.post(
        f"{BASE_URL}/v1/files",
        params={"provider": "anthropic"},
        data={"purpose": "assistants"},
        files={"file": ("tiny.pdf", tiny_pdf(), "application/pdf")},
    )
    response.raise_for_status()
    file_id = response.json()["id"]
    print(f"uploaded file_id={file_id}")
    return file_id


def delete_file(client: httpx.Client, file_id: str) -> None:
    response = client.delete(
        f"{BASE_URL}/v1/files/{quote(file_id, safe='')}",
        params={"provider": "anthropic"},
    )
    print(f"delete status={response.status_code}")


def main() -> None:
    uploaded = FILE_ID is None
    with httpx.Client(headers=headers(), timeout=60) as client:
        file_id = FILE_ID or upload_file(client)
        try:
            response = client.post(
                f"{BASE_URL}/litellm/v1/chat/completions",
                json={
                    "model": MODEL,
                    "max_tokens": 16,
                    "messages": [
                        {
                            "role": "user",
                            "content": [
                                {"type": "text", "text": "Read the attached PDF."},
                                {
                                    "type": "file",
                                    "file": {
                                        "file_id": file_id,
                                        "filename": "tiny.pdf",
                                    },
                                },
                            ],
                        }
                    ],
                },
            )
            print(f"chat status={response.status_code}")
            print(response.text)
        finally:
            if uploaded:
                delete_file(client, file_id)


if __name__ == "__main__":
    main()
