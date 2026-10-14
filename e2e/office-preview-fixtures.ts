// Synthetic OOXML packages for preview acceptance. No personal/business data.
// ZIP "store" entries keep these tiny fixtures dependency-free and inspectable.
function zip(parts: Record<string, string>): Buffer {
  const files: Buffer[] = [];
  const directory: Buffer[] = [];
  let offset = 0;
  for (const [filename, xml] of Object.entries(parts)) {
    const name = Buffer.from(filename);
    const data = Buffer.from(xml);
    let crc = 0xffffffff;
    for (const byte of data) {
      crc ^= byte;
      for (let i = 0; i < 8; i++) crc = (crc >>> 1) ^ ((crc & 1) ? 0xedb88320 : 0);
    }
    crc = (crc ^ 0xffffffff) >>> 0;
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt32LE(crc, 14);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(name.length, 26);
    const entry = Buffer.alloc(46);
    entry.writeUInt32LE(0x02014b50, 0);
    entry.writeUInt16LE(20, 4);
    entry.writeUInt16LE(20, 6);
    entry.writeUInt32LE(crc, 16);
    entry.writeUInt32LE(data.length, 20);
    entry.writeUInt32LE(data.length, 24);
    entry.writeUInt16LE(name.length, 28);
    entry.writeUInt32LE(offset, 42);
    files.push(local, name, data);
    directory.push(entry, name);
    offset += local.length + name.length + data.length;
  }
  const central = Buffer.concat(directory);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(Object.keys(parts).length, 8);
  end.writeUInt16LE(Object.keys(parts).length, 10);
  end.writeUInt32LE(central.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...files, central, end]);
}

const relNS = "http://schemas.openxmlformats.org/officeDocument/2006/relationships";
const relationships = (entries: string) => `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">${entries}</Relationships>`;
const relationship = (id: number, type: string, target: string) => `<Relationship Id="rId${id}" Type="${relNS}/${type}" Target="${target}"/>`;
const types = (entries: Record<string, string>) => `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/>${Object.entries(entries).map(([part, type]) => `<Override PartName="/${part}" ContentType="application/vnd.openxmlformats-officedocument.${type}"/>`).join("")}</Types>`;

export function spreadsheetFixture(): Buffer {
  const sheet = (label: string, value: number) => `<worksheet xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"><cols><col min="1" max="1" width="32" customWidth="1"/></cols><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>${label}</t></is></c><c r="B1"><v>${value}</v></c></row></sheetData></worksheet>`;
  return zip({
    "[Content_Types].xml": types({ "xl/workbook.xml": "spreadsheetml.sheet.main+xml", "xl/worksheets/sheet1.xml": "spreadsheetml.worksheet+xml", "xl/worksheets/sheet2.xml": "spreadsheetml.worksheet+xml" }),
    "_rels/.rels": relationships(relationship(1, "officeDocument", "xl/workbook.xml")),
    "xl/workbook.xml": `<workbook xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main" xmlns:r="${relNS}"><sheets><sheet name="Summary" sheetId="1" r:id="rId1"/><sheet name="Detail" sheetId="2" r:id="rId2" state="hidden"/></sheets></workbook>`,
    "xl/_rels/workbook.xml.rels": relationships(relationship(1, "worksheet", "worksheets/sheet1.xml") + relationship(2, "worksheet", "worksheets/sheet2.xml")),
    "xl/worksheets/sheet1.xml": sheet("Office preview 验收", 95),
    "xl/worksheets/sheet2.xml": sheet("Hidden sheet evidence", 42),
  });
}

export function slidesFixture(): Buffer {
  const slide = (text: string) => `<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main" xmlns:r="${relNS}" xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main"><p:cSld><p:spTree><p:nvGrpSpPr><p:cNvPr id="1" name=""/><p:cNvGrpSpPr/><p:nvPr/></p:nvGrpSpPr><p:grpSpPr/><p:sp><p:nvSpPr><p:cNvPr id="2" name="Preview title"/><p:cNvSpPr txBox="1"/><p:nvPr/></p:nvSpPr><p:spPr><a:xfrm><a:off x="914400" y="914400"/><a:ext cx="10000000" cy="2000000"/></a:xfrm><a:prstGeom prst="rect"><a:avLst/></a:prstGeom></p:spPr><p:txBody><a:bodyPr/><a:lstStyle/><a:p><a:r><a:rPr lang="en-US" sz="2800"/><a:t>${text}</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld><p:clrMapOvr><a:masterClrMapping/></p:clrMapOvr></p:sld>`;
  return zip({
    "[Content_Types].xml": types({ "ppt/presentation.xml": "presentationml.presentation.main+xml", "ppt/slides/slide1.xml": "presentationml.slide+xml", "ppt/slides/slide2.xml": "presentationml.slide+xml" }),
    "_rels/.rels": relationships(relationship(1, "officeDocument", "ppt/presentation.xml")),
    "ppt/presentation.xml": `<p:presentation xmlns:p="http://schemas.openxmlformats.org/presentationml/2006/main" xmlns:r="${relNS}"><p:sldIdLst><p:sldId id="256" r:id="rId1"/><p:sldId id="257" r:id="rId2"/></p:sldIdLst><p:sldSz cx="12192000" cy="6858000"/><p:notesSz cx="6858000" cy="9144000"/></p:presentation>`,
    "ppt/_rels/presentation.xml.rels": relationships(relationship(1, "slide", "slides/slide1.xml") + relationship(2, "slide", "slides/slide2.xml")),
    "ppt/slides/slide1.xml": slide("Office preview 验收 - slide one"),
    "ppt/slides/slide2.xml": slide("Reply attachment - slide two"),
  });
}
