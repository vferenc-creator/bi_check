# Adatbázis-térkép – kézi teszt valódi SQL Serverrel

A térkép logikáját, a tárolást (két gép egyszerre) és a sornézegetőt automatikus tesztek ellenőrzik a beépített `(demo)` adatbázissal. A valódi SQL Server-kapcsolatot csak a cégen belül lehet kipróbálni – ez a lista ehhez készült (kb. 10 perc).

## 1. Kapcsolat (egy gépen)

1. *Beállítások → Adatbázis (csak olvasás)*: szerver (pl. `EF-SQL01` vagy `EF-SQL01\PÉLDÁNY`), adatbázis, *Windows-bejelentkezés*.
2. *Kapcsolat tesztelése* → zöld doboz: szerver, adatbázis, a saját Windows-felhasználó, SQL Server verzió, táblák száma.
   - Ha sárga („írási joga is van”): a program így is csak olvas, de érdemes egy csak olvasási jogú felhasználót/csoportot kérni.
   - Ha hiba: lásd README → Hibaelhárítás (Adatbázis).
3. *Mentés*.

## 2. Szerkezet

1. *Adatbázis* fül → *Szerkezet lekérése*. A bal oldalon megjelennek a táblák és nézetek a becsült sorszámmal, a térképen a kártyák.
2. A definiált idegen kulcsok vonalai látszanak; az elsődleges kulcsok 🔑 jelet kapnak.
3. Ha az adatbázisban vannak `MS_Description` leírások, a részletpanelen „Az adatbázisban: …” sorként jelennek meg.
4. Ha a lekérés után piros figyelmeztetés jön („Részben nem olvasható”), az a jogosultság hiányát jelzi (pl. sorok száma) – a térkép ettől működik.

## 3. Leírások (meta.leiras_beallit)

1. *Kapcsolat tesztelése*: a „Leírások” sor szerint az eljárás elérhető és futtatható.
2. Egy táblán, amelynek SQL-ből már van leírása: a részletpanelen a *Leírás* és a mezők leírásai megjelennek.
3. Írjon át egy mezőleírást (aposztróffal, ékezettel, pl. `Az ügyfél 'fő' címe`), Enter → zöld villanás, „Mezőleírás mentve az adatbázisba”.
4. SSMS-ben ellenőrizze (a [docs/tabla_leirasok.md](tabla_leirasok.md) kiolvasó lekérdezésével), hogy pontosan ez a szöveg került be.
5. SQL-ből módosítson egy leírást, majd a programban *Újraolvasás* → az új szöveg látszik.
6. Ctrl+Z → a régi szöveg kerül vissza az adatbázisba.
7. *Beállítások → Adatbázis → Csak olvasás*: a leírás mezőn kilépve nincs mentés (a régi, térképes működés), az adatbázis nem változik.

## 4. Sorok

1. Egy kisebb táblán *Első 100 sor*: a dátum, szám (decimal), szöveg, GUID (uniqueidentifier) és NULL értékek helyesen jelennek meg.
2. Egy nagyobb táblán *Teljes tábla*: a betöltés közben számol („… / korlát sor betöltve”), *Megszakítás* működik.
   - A korlátot 10 000 000-ra állítva, mentés és újraindítás után is 10 000 000 marad.
   - Több millió soros táblán: a görgő, a Home/End és a görgetősáv húzása az utolsó sorig eljut; szűrés, keresés, rendezés néhány másodpercen belül kész.
   - A táblázat bezárása után a Feladatkezelőben a program memóriája visszacsökken.
3. Szűrők: `>0`, `=2024-03` (dátum oszlopon), `üres`; rendezés oszlopfejlécre kattintva.

## 5. Közös térkép (két gép, közös módban)

1. Az A gépen: egy tábla leírása, egy mezőmagyarázat, egy kapcsolat (pont húzása mezőről mezőre), egy csoport.
2. A B gépen ugyanarra az adatbázisra beállítva, az Adatbázis fül megnyitása után legfeljebb 20 mp-en belül megjelennek az A gép módosításai.
3. A közös mappában csak az új `adatbazis-terkep\` almappa jelent meg; a `bicheck.json`, `reports\`, `locks\` változatlan, a figyelt riportok mindkét gépen ugyanúgy működnek.
