# Táblák és mezők leírása (SQL Server)

## 1. Leírás írása táblához és mezőhöz

Tábla leírása:

```sql
EXEC meta.leiras_beallit '{sema}', '{tabla}', NULL, N'{tabla_leiras}';
```

Mező leírása (mezőnként egy sor):

```sql
EXEC meta.leiras_beallit '{sema}', '{tabla}', '{mezo}', N'{mezo_leiras}';
```

Szabályok:
- A mező NULL, ha a leírás a táblára vonatkozik.
- Először a tábla leírása jön, utána mezőnként egy sor.
- A leírás elé mindig N kell (N'...').
- A szövegben lévő aposztrófot duplázni kell ('').

Tartalmi konvenciók:
- Tábla: mit tartalmaz, melyik cégre vonatkozik, és mi egy sor
  (pl. "cikk × üzletág × időszak szinten egy sor").
- cikkszam: "Energofish cikkszám (a cikktörzs azonosítója)".
- idoszak: ÉÉÉÉ.HH formátum (pl. 2026.10); meg kell adni, hogy hóvégi pillanatkép
  vagy havi összesítés.
- Összeg: nettó/bruttó és pénznem.
- Mennyiség: mértékegység.
- Kódolt mező: példa értékekkel (pl. "1_NAGYKER, 9_KISKER").

### Példa: egy teljes tábla leírása

```sql
EXEC meta.leiras_beallit 'dbo', 'ertekesites_hovegi_ro', NULL, N'Cikkenkénti havi értékesítés a román cégre üzletágankénti bontásban, cikk × üzletág × időszak szinten egy sor';
EXEC meta.leiras_beallit 'dbo', 'ertekesites_hovegi_ro', 'cikkszam', N'Energofish cikkszám (a cikktörzs azonosítója)';
EXEC meta.leiras_beallit 'dbo', 'ertekesites_hovegi_ro', 'idoszak', N'Az értékesítés időszaka ÉÉÉÉ.HH formátumban (pl. 2026.02), a hónap összes értékesítése összesítve';
EXEC meta.leiras_beallit 'dbo', 'ertekesites_hovegi_ro', 'ertekesites_osszeg', N'Az időszak nettó értékesítési összege a cikkre és üzletágra, forintban';
EXEC meta.leiras_beallit 'dbo', 'ertekesites_hovegi_ro', 'mennyiseg', N'Az időszakban értékesített mennyiség alapmértékegységben';
EXEC meta.leiras_beallit 'dbo', 'ertekesites_hovegi_ro', 'uzletag', N'Az értékesítő üzletág kóddal és névvel (pl. 2_ROM_KÖZV)';
```

## 2. Leírás kiolvasása

Egy tábla mezői a leírásokkal (paraméterek: @sema, @tabla):

```sql
SELECT
    t.name                            AS tabla,
    CAST(tep.value AS NVARCHAR(4000)) AS tabla_leiras,
    c.column_id                       AS sorrend,
    c.name                            AS mezo,
    ty.name                           AS tipus,
    c.is_nullable                     AS ures_lehet,
    CAST(cep.value AS NVARCHAR(4000)) AS mezo_leiras
FROM sys.tables t
JOIN sys.columns c   ON c.object_id = t.object_id
JOIN sys.types ty    ON ty.user_type_id = c.user_type_id
LEFT JOIN sys.extended_properties tep
       ON tep.class = 1 AND tep.major_id = t.object_id
      AND tep.minor_id = 0 AND tep.name = 'MS_Description'
LEFT JOIN sys.extended_properties cep
       ON cep.class = 1 AND cep.major_id = t.object_id
      AND cep.minor_id = c.column_id AND cep.name = 'MS_Description'
WHERE SCHEMA_NAME(t.schema_id) = @sema
  AND t.name = @tabla
ORDER BY c.column_id;
```

- Az összes tábla listázásához a `t.name = @tabla` feltételt el kell hagyni.
- Ahol a mezo_leiras NULL, ott még nincs leírás.
- A lekérdezést paraméterezve kell futtatni, a táblanevet nem szabad a szövegbe fűzni.

### Példa: az ertekesites_hovegi_ro tábla leírásainak kiolvasása

```sql
DECLARE @sema  SYSNAME = N'dbo';
DECLARE @tabla SYSNAME = N'ertekesites_hovegi_ro';

SELECT
    t.name                            AS tabla,
    CAST(tep.value AS NVARCHAR(4000)) AS tabla_leiras,
    c.column_id                       AS sorrend,
    c.name                            AS mezo,
    ty.name                           AS tipus,
    c.is_nullable                     AS ures_lehet,
    CAST(cep.value AS NVARCHAR(4000)) AS mezo_leiras
FROM sys.tables t
JOIN sys.columns c   ON c.object_id = t.object_id
JOIN sys.types ty    ON ty.user_type_id = c.user_type_id
LEFT JOIN sys.extended_properties tep
       ON tep.class = 1 AND tep.major_id = t.object_id
      AND tep.minor_id = 0 AND tep.name = 'MS_Description'
LEFT JOIN sys.extended_properties cep
       ON cep.class = 1 AND cep.major_id = t.object_id
      AND cep.minor_id = c.column_id AND cep.name = 'MS_Description'
WHERE SCHEMA_NAME(t.schema_id) = @sema
  AND t.name = @tabla
ORDER BY c.column_id;
```

Várható eredmény (a tabla_leiras minden sorban ugyanaz, itt rövidítve):

| tabla | sorrend | mezo | tipus | mezo_leiras |
|---|---|---|---|---|
| ertekesites_hovegi_ro | 1 | cikkszam | nvarchar | Energofish cikkszám (a cikktörzs azonosítója) |
| ertekesites_hovegi_ro | 2 | idoszak | nvarchar | Az értékesítés időszaka ÉÉÉÉ.HH formátumban (pl. 2026.02), a hónap összes értékesítése összesítve |
| ertekesites_hovegi_ro | 3 | ertekesites_osszeg | float | Az időszak nettó értékesítési összege a cikkre és üzletágra, forintban |
| ertekesites_hovegi_ro | 4 | mennyiseg | float | Az időszakban értékesített mennyiség alapmértékegységben |
| ertekesites_hovegi_ro | 5 | uzletag | nvarchar | Az értékesítő üzletág kóddal és névvel (pl. 2_ROM_KÖZV) |
