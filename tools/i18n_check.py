#!/usr/bin/env python3
"""Checks the English of the interface.

  python3 tools/i18n_check.py [files...]

Lists (1) t()/tn() keys with no entry in frontend/src/i18n/en/*.json and
(2) Portuguese that looks left outside t() (JSX text and string literals).
With no files, checks all of frontend/src. Exit 1 when (1) has anything.
"""
import json, glob, os, re, sys

ROOT = os.path.join(os.path.dirname(os.path.abspath(__file__)), "..", "frontend", "src")
STR = r'"((?:[^"\\\n]|\\.)*)"'
T_CALL = re.compile(r'\bt\(\s*' + STR)
TN_CALL = re.compile(r'\btn\(\s*[^,]+,\s*' + STR + r'\s*,\s*' + STR)
PT = re.compile(r'[ãõçáéíóúâêôà]|\b(não|nao|para|com|uma|está|sem|mais|já|ainda|foi|são|pelo|pela|do|da|dos|das|no|na|nos|nas|em|de|que|ou|o|os|as|ao|aos|um|seu|sua|este|esta|esse|essa|aqui|agora|abrir|salvar|apagar|adicionar|remover|parar|iniciar|criar|editar|cancelar|fechar|voltar|nenhum|nenhuma|todos|todas|hora|horas|dias|minutos|máquina|pasta|arquivo|senha|usuário|projeto)\b', re.I)

def unescape(s):
    return json.loads('"' + s + '"')

def dict_keys():
    keys = {}
    for f in glob.glob(os.path.join(ROOT, "i18n", "en", "*.json")):
        with open(f, encoding="utf-8") as fh:
            keys.update(json.load(fh))
    return keys

def strip_comments(src):
    src = re.sub(r'/\*.*?\*/', lambda m: re.sub(r'[^\n]', ' ', m.group(0)), src, flags=re.S)
    return re.sub(r'(^|[^:"\'])//[^\n]*', r'\1', src)

def check(files):
    en = dict_keys()
    missing, loose = [], []
    for f in files:
        src = strip_comments(open(f, encoding="utf-8").read())
        used = set()
        for m in T_CALL.finditer(src):
            used.add(unescape(m.group(1)))
        for m in TN_CALL.finditer(src):
            used.add(unescape(m.group(1))); used.add(unescape(m.group(2)))
        for k in sorted(used):
            if k not in en or not en[k]:
                missing.append((f, k))
        # Portuguese outside t(): JSX text, and string literals not in t()/tn().
        inside = set()
        for m in list(T_CALL.finditer(src)) + list(TN_CALL.finditer(src)):
            for g in range(1, m.lastindex + 1):
                inside.add(m.start(g))
        for i, line in enumerate(src.split("\n"), 1):
            for m in re.finditer(r'>\s*([^<>{}\n]*[A-Za-zÀ-ú]{2,}[^<>{}\n]*?)\s*<', line):
                txt = m.group(1).strip()
                if txt and PT.search(txt) and not re.match(r'^[\w.-]+$', txt):
                    loose.append((f, i, txt))
        offs = 0
        for i, line in enumerate(src.split("\n"), 1):
            for m in re.finditer(STR, line):
                s = m.group(1)
                if offs + m.start(1) in inside:
                    continue
                if re.search(r'\bimport\b|\bfrom\s*$', line[:m.start()]):
                    continue
                if " " in s and PT.search(s) and not re.match(r'^[#/.@]|^[a-z-]+ [a-z- ]+$', s) and "className" not in line[max(0, m.start()-12):m.start()]:
                    loose.append((f, i, s))
            offs += len(line) + 1
    return missing, loose

if __name__ == "__main__":
    files = sys.argv[1:] or [f for f in glob.glob(os.path.join(ROOT, "**", "*.ts*"), recursive=True) if "/i18n/" not in f]
    missing, loose = check(files)
    for f, k in missing:
        print("SEM INGLÊS  %s: %r" % (os.path.relpath(f), k))
    for f, i, s in loose:
        print("FORA DO t()  %s:%d: %s" % (os.path.relpath(f), i, s[:100]))
    print("%d sem inglês, %d possíveis textos fora do t()" % (len(missing), len(loose)))
    sys.exit(1 if missing else 0)
