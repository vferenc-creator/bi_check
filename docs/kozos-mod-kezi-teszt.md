# Közös mód – kézi tesztlista két valódi gépre

Az automata tesztek (`internal/teamstore`, `internal/app`) ideiglenes mappán, több párhuzamos példánnyal ellenőrzik a zárolást, az ütközéseket, a félbeszakadt írást, a sémaeltérést és a mappa eltűnését; a CI ezeket Windowson (NTFS) is lefuttatja. **Amit csak valódi hálózaton lehet kipróbálni**, az alább következik.

**Előkészítés**
- Két gép: **A** (pl. Kiss Anna, EF-PC12) és **B** (pl. Varga Ferenc, EF-PC07), mindkettőn ugyanaz a programverzió (Névjegy oldal).
- Közös mappa: pl. `\\EFS-FSRHQ\Groups\BI\BI_Check_kozos` – mindkét felhasználónak olvasási **és írási** joga van.
- Mindkét gépen: *Beállítások → Közös mód → Frissítési időköz* = 10 mp (a teszt idejére), *Zár lejárati ideje* = 2 perc.

Jelölés: ☐ = kipróbálandó, mellé írja az eredményt / eltérést.

---

## 1. Bekapcsolás és átköltözés
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 1.1 | **A**: Beállítások → Közös mód → a mappát **M:\\…** formában tallózza ki → Ellenőrzés | UNC-re alakul, „Üres mappa – a program előkészíti…” | ☐ |
| 1.2 | **A**: Bekapcsolás → az átköltözés ablakban minden „átmásolás” | Az elemek megjelennek, a mappában `bicheck.json`, `reports\`, `locks\` | ☐ |
| 1.3 | **B**: ugyanazt a mappát beírja `"…"` idézőjelek között (Intéző „Másolás elérési útként”) | Az idézőjeleket levágja; „Meglévő közös mappa, N riporttal” | ☐ |
| 1.4 | **B**: bekapcsolás; B-nek van egy riportja, ami A-nál is megvan (ugyanaz az útvonal) | Az átköltözésnél „Már van ilyen…”, alapértelmezés: kihagyás | ☐ |
| 1.5 | Olyan mappát ad meg, amihez csak olvasási joga van | „A mappa nem írható…”, nem kapcsol be | ☐ |

## 2. Felvétel, módosítás, jelzés a másik gépen
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 2.1 | **A**: új riport felvétele | **B**-n ≤10 mp múlva megjelenik, „Új riport: X – felvette: Kiss Anna” jelzés, „új” címke | ☐ |
| 2.2 | **A**: módosítja egy riport türelmi idejét | **B**-n „Módosított riport…”, a részletpanel változásnaplójában: „Kiss Anna módosította: türelmi idő” | ☐ |
| 2.3 | **B**: kikapcsol (figyelés ki) egy riportot | **A**-n is „Kikapcsolva” lesz | ☐ |

## 3. Zárolás
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 3.1 | **A**: megnyit egy riportot szerkesztésre (nem ment) | A szerkesztő fejlécében „zárolva Önnek” | ☐ |
| 3.2 | **B**: ugyanazt a riportot nézi a listában | Lakat + „Szerkeszti: Kiss Anna” | ☐ |
| 3.3 | **B**: megnyitja a riportot | „Riport megtekintése”, sáv: „Szerkeszti: Kiss Anna (EF-PC12), HH:MM óta”, mezők tiltva, nincs Mentés | ☐ |
| 3.4 | **A** és **B** szinte egyszerre kattint ugyanannak a riportnak a Szerkesztés gombjára | Csak az egyik kap szerkesztőt, a másik megtekintő módot | ☐ |
| 3.5 | **A**: Mégse | **B**-n ≤10 mp múlva eltűnik a lakat | ☐ |
| 3.6 | **A**: szerkesztés közben bezárja a főablakot (X) | A zár feloldódik (B-n eltűnik a lakat) | ☐ |
| 3.7 | **A**: szerkesztés közben kilép a tálcamenüből | A zár feloldódik | ☐ |
| 3.8 | **B**: lomtárba tenné azt a riportot, amit A szerkeszt | Hibaüzenet: „…éppen szerkeszti: Kiss Anna…” | ☐ |

## 4. Elárvult zár
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 4.1 | **A**: szerkesztés közben **Feladatkezelőből kilövi** a BIMonitor.exe-t | **B**-n a lakat megmarad … | ☐ |
| 4.2 | … 2 perc (a beállított lejárat) után **B** megnyitja | **B** szerkesztheti (a lejárt zárat átvette); a naplóban (`monitor.log`) „zár feloldva … lejárt” | ☐ |
| 4.3 | **A**: újraindítja a programot, miközben a saját régi zára még ott van | Induláskor feloldja a saját régi zárát, azonnal szerkeszthet | ☐ |
| 4.4 | **A**: szerkesztés közben **kihúzza a hálózati kábelt / lecsatlakozik a VPN-ről** 3 percre | **B** 2 perc után átveheti a zárat. **A**: a kapcsolat visszatérésekor „A szerkesztési zárat elvesztette” jelzés; mentéskor ütközés-ablak vagy „a zárat … vette át” üzenet – **csendes felülírás nincs** | ☐ |
| 4.5 | **B**: kézi „Zár feloldása” A élő zárán (megerősítéssel) | Feloldja; a riport változásnaplójában „Varga Ferenc feloldotta a zárat – korábban: Kiss Anna…” | ☐ |
| 4.6 | **A**: gép alvó módba teszi szerkesztés közben, 3 perc múlva felébreszti | Mint 4.4 | ☐ |

## 5. Ütközés
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 5.1 | **A** szerkeszt; **B** kézzel feloldja a zárat, maga is szerkeszt és ment; **A** ment | **A**-nál ütközés-ablak: mezőnkénti eltérés, „A közös mappában (Varga Ferenc, HH:MM)” | ☐ |
| 5.2 | 5.1-ben „Az övét tartom meg” | A szerkesztő bezárul, a listában B változata | ☐ |
| 5.3 | 5.1-ben „Az enyémet mentem” | A változata kerül a mappába, a naplóban látszik | ☐ |

## 6. Lomtár
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 6.1 | **A**: Lomtárba helyez egy riportot | **B**-n eltűnik a listából, a Lomtárban: „Kiss Anna (EF-PC12), időpont” | ☐ |
| 6.2 | **B**: Visszaállítás | Mindkét gépen visszakerül, az előzmények megvannak | ☐ |
| 6.3 | **B**: Végleges törlés megerősítéssel | A fájl eltűnik a `reports\` mappából | ☐ |

## 7. Hálózati kiesés
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 7.1 | **B**: lecsatlakozik a hálózatról / VPN-ről | rövidesen (a frissítési időköz + legfeljebb ~30 mp időkorlát után) „Offline mód” sáv; a lista megmarad, a figyelés fut (a helyi fájlok OK, a hálózatiak Elérhetetlen) | ☐ |
| 7.2 | **B** offline: Új elem / Szerkesztés / Lomtár | Tiltva, magyarázó üzenettel | ☐ |
| 7.3 | **B** offline: a programot újraindítja | A lista a gyorsítótárból azonnal megjelenik | ☐ |
| 7.4 | **B**: hálózat vissza | Automatikus frissítés, „A közös mappa újra elérhető” | ☐ |
| 7.5 | A közös mappa jogosultságát ideiglenesen elveszi B-től | „nincs jogosultság…” offline okként | ☐ |

## 8. Személyes beállítások
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 8.1 | **A**: egy riportnál „Értesítés nekem” ki | **A** nem kap róla értesítést, **B** igen | ☐ |
| 8.2 | **B**: Beállítások → Értesítések → egy csoport kikapcsolása | Csak **B**-re hat | ☐ |
| 8.3 | **A**: Munkanaptár → új pihenőnap | **B**-n is megjelenik (közös) | ☐ |

## 9. Programverziók
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 9.1 | Későbbi, formátumot emelő verziónál: **A** frissít, **B** még a régit futtatja | **B**: „Ezt a közös mappát egy újabb programverzió kezeli… csak olvasni tud” – nem ír felül semmit | ☐ |

## 10. Terhelés / tartósság (opcionális)
| # | Lépés | Elvárt eredmény | ☐ |
|---|---|---|---|
| 10.1 | 1 napig mindkét gépen fut közös módban | Nincs 1 óránál régebbi `.tmp-*` fájl a `reports\` mappában (ezeket a program takarítja) | ☐ |
| 10.2 | Vírusirtó aktív a fájlszerveren | Nincs „fájl használatban” hiba a naplóban (átmeneti hibánál a program újrapróbál) | ☐ |

---

**Hibajelentéshez** csatolja: a két gép `%LOCALAPPDATA%\EnergofishMonitor\logs\monitor.log` fájlját, a közös mappa `locks\` tartalmáról egy képernyőképet, és a programverziót (Névjegy).
