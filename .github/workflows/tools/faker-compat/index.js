// Compatibility shim: exposes the @faker-js/faker v5 API that
// postman-collection/lib/superstring/dynamic-variables.js calls, backed by
// @faker-js/faker 10.x (aliased as "faker10" so the root override that points
// "@faker-js/faker" at this package does not also rewrite our own dependency).
//
// Every postman-collection release, including the latest, pins faker 5.5.3,
// which is affected by GHSA-qxc2-j82w-r537. Newman only ever calls the
// generators below with fixed arguments, so this mapping is the whole surface.
//
// The override in ../package.json points "@faker-js/faker" at this directory.
// Regenerate package-lock.json with npm 11 or newer: npm 10 resolves a file:
// override relative to the dependent package instead of the project root and
// writes a dangling link into the lockfile. Installing from the lock works
// with either npm version.
//
// faker 10 ships ES modules only; Node 22.13+ (CI runs Node 25) can require()
// them natively, which is why the engines floor matches faker's own.
'use strict';

const { faker } = require('faker10/locale/en');

// faker v5 exposed company suffixes from locale data; v8+ removed the API.
const COMPANY_SUFFIXES = ['Inc', 'and Sons', 'LLC', 'Group'];

const numberInt = (opts) => {
  if (typeof opts === 'number') {
    return faker.number.int({ max: opts });
  }
  return faker.number.int(opts === undefined ? { max: 99999 } : opts);
};

// faker v9 removed the per-category image helpers; every one returns a placeholder URL.
const imageUrl = () => faker.image.url();

const phoneFormat = () =>
  `${faker.string.numeric(3)}-${faker.string.numeric(3)}-${faker.string.numeric(4)}`;

module.exports = {
  address: {
    city: () => faker.location.city(),
    country: () => faker.location.country(),
    countryCode: () => faker.location.countryCode(),
    latitude: () => faker.location.latitude().toFixed(4),
    longitude: () => faker.location.longitude().toFixed(4),
    streetAddress: () => faker.location.streetAddress(),
    streetName: () => faker.location.street(),
  },
  commerce: {
    color: () => faker.color.human(),
    department: () => faker.commerce.department(),
    product: () => faker.commerce.product(),
    productAdjective: () => faker.commerce.productAdjective(),
    productMaterial: () => faker.commerce.productMaterial(),
    productName: () => faker.commerce.productName(),
  },
  company: {
    bs: () => faker.company.buzzPhrase(),
    bsAdjective: () => faker.company.buzzAdjective(),
    bsBuzz: () => faker.company.buzzVerb(),
    bsNoun: () => faker.company.buzzNoun(),
    catchPhrase: () => faker.company.catchPhrase(),
    catchPhraseAdjective: () => faker.company.catchPhraseAdjective(),
    catchPhraseDescriptor: () => faker.company.catchPhraseDescriptor(),
    catchPhraseNoun: () => faker.company.catchPhraseNoun(),
    companyName: () => faker.company.name(),
    companySuffix: () => faker.helpers.arrayElement(COMPANY_SUFFIXES),
  },
  database: {
    collation: () => faker.database.collation(),
    column: () => faker.database.column(),
    engine: () => faker.database.engine(),
    type: () => faker.database.type(),
  },
  datatype: {
    boolean: () => faker.datatype.boolean(),
    number: numberInt,
    uuid: () => faker.string.uuid(),
  },
  date: {
    future: () => faker.date.future(),
    month: () => faker.date.month(),
    past: () => faker.date.past(),
    recent: () => faker.date.recent(),
    weekday: () => faker.date.weekday(),
  },
  finance: {
    account: () => faker.finance.accountNumber(),
    accountName: () => faker.finance.accountName(),
    amount: () => faker.finance.amount(),
    bic: () => faker.finance.bic(),
    bitcoinAddress: () => faker.finance.bitcoinAddress(),
    currencyCode: () => faker.finance.currencyCode(),
    currencyName: () => faker.finance.currencyName(),
    currencySymbol: () => faker.finance.currencySymbol(),
    iban: () => faker.finance.iban(),
    // v5 finance.mask() default output: four digits wrapped as (...1234)
    mask: () => `(...${faker.string.numeric(4)})`,
    transactionType: () => faker.finance.transactionType(),
  },
  hacker: {
    abbreviation: () => faker.hacker.abbreviation(),
    adjective: () => faker.hacker.adjective(),
    ingverb: () => faker.hacker.ingverb(),
    noun: () => faker.hacker.noun(),
    phrase: () => faker.hacker.phrase(),
    verb: () => faker.hacker.verb(),
  },
  image: {
    abstract: imageUrl,
    animals: imageUrl,
    avatar: () => faker.image.avatar(),
    business: imageUrl,
    cats: imageUrl,
    city: imageUrl,
    dataUri: () => faker.image.dataUri(),
    fashion: imageUrl,
    food: imageUrl,
    imageUrl: () => faker.image.url(),
    nature: imageUrl,
    nightlife: imageUrl,
    people: imageUrl,
    sports: imageUrl,
    transport: imageUrl,
  },
  internet: {
    color: () => faker.color.rgb({ format: 'hex', casing: 'lower' }),
    domainName: () => faker.internet.domainName(),
    domainSuffix: () => faker.internet.domainSuffix(),
    domainWord: () => faker.internet.domainWord(),
    email: () => faker.internet.email(),
    exampleEmail: () => faker.internet.exampleEmail(),
    ip: () => faker.internet.ip(),
    ipv6: () => faker.internet.ipv6(),
    mac: () => faker.internet.mac(),
    password: () => faker.internet.password(),
    protocol: () => faker.internet.protocol(),
    url: () => faker.internet.url(),
    userAgent: () => faker.internet.userAgent(),
    userName: () => faker.internet.username(),
  },
  lorem: {
    lines: () => faker.lorem.lines(),
    paragraph: () => faker.lorem.paragraph(),
    paragraphs: () => faker.lorem.paragraphs(),
    sentence: () => faker.lorem.sentence(),
    sentences: () => faker.lorem.sentences(),
    slug: () => faker.lorem.slug(),
    text: () => faker.lorem.text(),
    word: () => faker.lorem.word(),
    words: () => faker.lorem.words(),
  },
  name: {
    findName: () => faker.person.fullName(),
    firstName: () => faker.person.firstName(),
    jobArea: () => faker.person.jobArea(),
    jobDescriptor: () => faker.person.jobDescriptor(),
    jobTitle: () => faker.person.jobTitle(),
    jobType: () => faker.person.jobType(),
    lastName: () => faker.person.lastName(),
    prefix: () => faker.person.prefix(),
    suffix: () => faker.person.suffix(),
  },
  phone: {
    phoneNumber: () => faker.phone.number(),
    phoneNumberFormat: phoneFormat,
  },
  random: {
    alphaNumeric: (count) => faker.string.alphanumeric(count === undefined ? 1 : count),
    arrayElement: (array) => faker.helpers.arrayElement(array),
    word: () => faker.word.sample(),
  },
  system: {
    commonFileExt: () => faker.system.commonFileExt(),
    commonFileName: () => faker.system.commonFileName(),
    commonFileType: () => faker.system.commonFileType(),
    fileExt: () => faker.system.fileExt(),
    fileName: () => faker.system.fileName(),
    fileType: () => faker.system.fileType(),
    mimeType: () => faker.system.mimeType(),
    semver: () => faker.system.semver(),
  },
};
