#!/usr/bin/env python3
"""Smoke test of the interface: opens every screen of a running Gantry
(started with -web) and fails on JavaScript errors or empty screens.

    pip install playwright && playwright install chromium
    ./dist/gantry -web -no-browser          # copy the link it prints
    python3 tests/ui_smoke.py 'http://127.0.0.1:7777/?c=…'

The link opens once (?c=); a link with the token itself (?t=) also works.
GANTRY_LANG=en (or pt) opens the app in that language; in English, words
of Portuguese left on a screen are listed (they need a t() or a dictionary
entry), without failing the run.
"""
import os
import json
import re
import sys
import urllib.parse
import urllib.request

from playwright.sync_api import sync_playwright

if len(sys.argv) < 2:
    sys.exit(__doc__)
link = sys.argv[1]
base = link.split("/?")[0]
query = urllib.parse.parse_qs(urllib.parse.urlparse(link).query)
if "t" in query:
    token = query["t"][0]
else:
    # The one-use link answers a page that hands the token to the browser.
    with urllib.request.urlopen(link, timeout=30) as r:
        token = re.search(r'setItem\("gantry\.token","([^"]+)"', r.read().decode()).group(1)
link = base + "/?t=" + token


def api(path):
    req = urllib.request.Request(base + "/api" + path, headers={"Authorization": "Bearer " + token})
    with urllib.request.urlopen(req, timeout=60) as r:
        return json.load(r)


routes = ["#/bem-vindo", "#/local/t/ferramentas", "#/local", "#/local/novo", "#/local/t/containers", "#/local/t/vms", "#/local/t/servicos", "#/local/t/engines", "#/local/t/config",
          "#/config/maquinas", "#/config/pastas", "#/config/credenciais", "#/config/geral",
          "#/modelos", "#/modelos/novo", "#/modelos/gantry-postgres", "#/modelos/gantry-misto/gerar", "#/modelos/gantry-paperclip/gerar"]
for h in api("/hosts"):
    if not h.get("ok"):
        continue
    hn = urllib.parse.quote(h["name"])
    eng = urllib.parse.quote(h["name"])
    routes += [f"#/h/{hn}", f"#/h/{hn}/novo", f"#/h/{hn}/t/vms", f"#/h/{hn}/t/servicos", f"#/h/{hn}/t/config", f"#/h/{hn}/criar", f"#/h/{hn}/construir",
               f"#/h/{hn}/t/containers/{eng}/images", f"#/h/{hn}/t/containers/{eng}/volumes", f"#/h/{hn}/t/containers/{eng}/networks"]
    ctrs = [c for lab in api(f"/hosts/{hn}/labs") for c in lab.get("containers", [])]
    if ctrs:
        c = ctrs[0]
        rt = c.get("runtime") if c.get("runtime") and c.get("runtime") != h.get("runtime") else "-"
        cp = f"#/h/{hn}/c/{urllib.parse.quote(rt)}/{c['id']}"
        routes += [cp, cp + "/editar", cp + "/duplicar"]
    for lab in api(f"/hosts/{hn}/labs")[:3]:
        ln = urllib.parse.quote(lab["id"])
        routes.append(f"#/h/{hn}/l/{ln}")
        if not lab.get("loose"):
            routes += [f"#/h/{hn}/l/{ln}/arquivo", f"#/h/{hn}/l/{ln}/topologia", f"#/h/{hn}/l/{ln}/metricas", f"#/h/{hn}/l/{ln}/backup", f"#/h/{hn}/l/{ln}/servicos"]

# Network labs: the drawing of the cables must show every node and cable.
cable_labs = []
for h in api("/hosts"):
    if not h.get("ok") or not h.get("files", True):
        continue
    hn = urllib.parse.quote(h["name"])
    for lab in api(f"/hosts/{hn}/labs"):
        if lab.get("loose") or len(cable_labs) >= 2:
            continue
        try:
            nodes = api(f"/hosts/{hn}/labs/{urllib.parse.quote(lab['id'])}/cabos")
        except Exception:
            continue
        if nodes:
            links = {f["link"] for n in nodes for f in n["interfaces"] if f.get("link")}
            cable_labs.append((f"#/h/{hn}/l/{urllib.parse.quote(lab['id'])}/topologia", len(nodes), len(links)))

details = []  # lists whose first item opens the details panel
for c in api("/clusters"):
    cid = urllib.parse.quote(c["id"])
    kinds = ["pods", "deployments", "cronjobs", "services", "secrets", "configmaps", "nodes", "events", "helm"]
    # The first custom resource of the cluster, if any.
    crds = api(f"/clusters/{cid}/crds").get("kinds", [])
    kinds += [k["id"] for k in crds[:1]]
    routes += [f"#/k/{cid}"] + [f"#/k/{cid}/{k}" for k in kinds]
    details += [f"#/k/{cid}/{k}" for k in kinds if k not in ("events",)]
routes.append("#/config/clusters")

failed = []
with sync_playwright() as p:
    browser = p.chromium.launch()
    lang = os.environ.get("GANTRY_LANG", "")
    page = browser.new_page(viewport={"width": 1280, "height": 860}, locale="en-US" if lang == "en" else "pt-BR")
    if lang:
        page.add_init_script(f"try{{localStorage.setItem('gantry.lang','{lang}')}}catch(e){{}}")
    leftovers = {}
    pt_words = re.compile(r"[ãõçáéíóúâêô]|\b(não|para|com|uma|está|sem|mais|já|ainda|foi|são|pelo|pela|dos|das|nos|nas|ao|aos|você|aqui|agora|nenhum|nenhuma|máquina|pasta|arquivo|projeto|abrir|salvar|apagar|adicionar|remover|parar|iniciar|criar|editar|cancelar|fechar|voltar)\b", re.I)
    errors = []
    page.on("pageerror", lambda e: errors.append(str(e)))
    page.goto(link)
    page.wait_for_timeout(1500)
    for theme in ("light", "dark"):
        page.evaluate(f"document.documentElement.setAttribute('data-theme', '{theme}')")
        for r in routes:
            errors.clear()
            page.goto(base + "/" + r)
            page.wait_for_timeout(1800)
            text = page.inner_text("#view").strip()
            problem = errors[:] or ([] if text else ["tela vazia"])
            if lang == "en" and theme == "light":
                full = page.inner_text("body")
                for line in full.split("\n"):
                    if pt_words.search(line):
                        leftovers.setdefault(line.strip()[:120], r)
            status = "ok" if not problem else "FALHOU: " + "; ".join(problem)
            print(f"[{theme}] {r}: {status}")
            if problem:
                failed.append(r)
    for r, n_nodes, n_cables in cable_labs:
        errors.clear()
        page.goto(base + "/" + r)
        problem = []
        try:
            page.wait_for_selector(".cablemap-svg", timeout=30000)
            page.wait_for_timeout(500)
            got = (page.locator(".cm-node").count(), page.locator(".cm-cable:not(.cm-member)").count())
            if got != (n_nodes, n_cables):
                problem.append(f"desenho com {got[0]} nós e {got[1]} cabos (o compose tem {n_nodes} e {n_cables})")
        except Exception as e:
            problem.append("sem o desenho dos cabos: " + str(e).split("\n")[0])
        problem = errors[:] + problem
        print(f"[cabos] {r}: " + ("ok" if not problem else "FALHOU: " + "; ".join(problem)))
        if problem:
            failed.append("cabos " + r)
    # The command palette: Ctrl K, a few letters, Enter goes there.
    errors.clear()
    page.goto(base + "/#/")
    page.wait_for_timeout(1500)
    page.keyboard.press("Control+k")
    page.wait_for_timeout(300)
    page.keyboard.type("config clusters")
    page.wait_for_timeout(400)
    page.keyboard.press("Enter")
    page.wait_for_timeout(800)
    problem = errors[:]
    if not page.evaluate("location.hash").startswith("#/config/clusters"):
        problem.append("a paleta não levou a Configurações › Clusters (" + page.evaluate("location.hash") + ")")
    if page.locator(".pal").count():
        problem.append("a paleta ficou aberta")
    print("[paleta] Ctrl K: " + ("ok" if not problem else "FALHOU: " + "; ".join(problem)))
    if problem:
        failed.append("paleta")
    # The details panel of the first item of each list.
    for r in details:
        errors.clear()
        page.goto(base + "/" + r)
        page.wait_for_timeout(1800)
        first = page.locator("#kube-view .kube-grid a").first
        if not first.count():
            print(f"[detalhe] {r}: lista vazia, pulado")
            continue
        first.click()
        page.wait_for_timeout(1800)
        body = page.locator(".kd-body")
        text = body.inner_text().strip() if body.count() else ""
        problem = errors[:] or ([] if text and "Carregando" not in text[:20] else ["painel vazio"])
        if page.locator(".kd-body .error-box").count():
            problem.append(page.locator(".kd-body .error-box").inner_text())
        print(f"[detalhe] {r}: " + ("ok" if not problem else "FALHOU: " + "; ".join(problem)))
        if problem:
            failed.append("detalhe " + r)
        page.keyboard.press("Escape")
    browser.close()

if leftovers:
    print(f"\n{len(leftovers)} linhas com português na interface em inglês:")
    for line, r in sorted(leftovers.items(), key=lambda x: x[1]):
        print(f"  {r}: {line}")
total = len(routes) * 2 + len(details) + len(cable_labs) + 1
print(f"\n{total - len(failed)} de {total} telas sem erro")
sys.exit(1 if failed else 0)
