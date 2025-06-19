// /// script
// {
//   "dependencies": {
//     "commander": "latest"
//   }
// }
// ///

import { Command } from 'commander';

// Print commander version at runtime
const commanderPackage = require('commander/package.json');
console.log(`Commander version: ${commanderPackage.version}`);

// Print debug information
console.log(`NODE_PATH: ${process.env.NODE_PATH || 'undefined'}`);
console.log(`Current working directory: ${process.cwd()}`);
console.log(`Command line arguments: ${JSON.stringify(process.argv)}`);

const program = new Command();

program
  .name('bunv')
  .description('Simple CLI tool')
  .version('1.0.0');

program
  .command('hello')
  .description('Say hello')
  .option('-n, --name <name>', 'name to greet', 'World')
  .action((options) => {
    console.log(`Hello, ${options.name}!`);
  });

program
  .command('list')
  .description('List items')
  .option('-f, --format <format>', 'output format', 'table')
  .action((options) => {
    console.log(`Listing items in ${options.format} format`);
  });

program.parse();
