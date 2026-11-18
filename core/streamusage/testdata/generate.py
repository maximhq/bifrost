"""Generate offline tokenizer parity fixtures. Run with tiktoken==0.12.0 and tokenizers==0.22.2.
Vocabularies and their upstream revisions are recorded in ../assets/manifest.json.
"""
import gzip, json
from pathlib import Path
import tiktoken
from tokenizers import Tokenizer
root=Path(__file__).resolve().parent
texts=["", "hello world", "think carefully", "café 你好 👩‍💻", "<|endoftext|>", "{\"city\":\"Paris\"}", "a"*2050,
       "Ｆｕｌｌｗｉｄｔｈ ①② ﬁ ¼ ™ \u00a0nbsp\u200bzw", "日本語のテキスト、カタカナ。中文测试。한국어", "It'S we'll 12345678 3.14159 0x1F",
       "  lead\t\ttabs\n\n\r\n   trail   ", "func main() {\n\tfmt.Println(\"hi\")\n}\n",
       "a <s> b</s><unk>", "x<EOT>y<META>", "<|begin_of_text|>hi<|eot_id|>", "<｜begin▁of▁sentence｜>hi<｜end▁of▁sentence｜>"]
cases=[]
for name in ["cl100k_base","o200k_base","anthropic","deepseek","llama2","llama3"]:
    if name.endswith("_base"):
        enc=tiktoken.get_encoding(name)
        count=lambda text:sum(len(enc.encode(text[i:i+1024],disallowed_special=())) for i in range(0,len(text),1024))
    else:
        asset=root.parent/"assets"/(name+".json.gz")
        enc=Tokenizer.from_str(gzip.decompress(asset.read_bytes()).decode())
        count=lambda text:len(enc.encode(text).ids)
    for text in texts:cases.append(dict(tokenizer=name,text=text,tokens=count(text)))
(root/"tokenizers.json").write_text(json.dumps(cases,ensure_ascii=False,indent=2)+"\n")
